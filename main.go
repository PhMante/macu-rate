package main

import (
	"bytes"
	"database/sql"
	"html/template"
	"image"
	"image/jpeg"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"

	_ "github.com/lib/pq"
	"github.com/rwcarlsen/goexif/exif"
	"golang.org/x/image/draw"
)

var db *sql.DB
var adminPassword string

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL environment variable not set")
	}
	var err error
	db, err = sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatal(err)
	}
	if err = db.Ping(); err != nil {
		log.Fatal(err)
	}

	adminPassword = os.Getenv("ADMIN_PASSWORD")
	if adminPassword == "" {
		log.Fatal("ADMIN_PASSWORD environment variable not set")
	}

	createTables()

	http.HandleFunc("/", homeHandler)
	http.HandleFunc("/admin", adminHandler)
	http.HandleFunc("/admin/add", adminAddHandler)
	http.HandleFunc("/admin/sort", adminSortHandler)
	http.HandleFunc("/vote", voteHandler)
	http.HandleFunc("/comments", commentsHandler)
	http.HandleFunc("/images/", imageHandler)

	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Println("Listening on port", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

// Set the global sort order (admin-only)
func adminSortHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	pass := r.FormValue("pass")
	if pass != adminPassword {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	order := r.FormValue("order")
	// Whitelist supported orders
	switch order {
	case "name", "score_desc", "upvotes_desc":
		// ok
	default:
		http.Error(w, "Invalid sort order", http.StatusBadRequest)
		return
	}

	if _, err := db.Exec("UPDATE settings SET value=$1 WHERE key='sort_order'", order); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin?pass="+pass, http.StatusSeeOther)
}

// Record a vote with optional comment
func voteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	personIDStr := r.FormValue("person_id")
	personID, err := strconv.Atoi(personIDStr)
	if err != nil || personID <= 0 {
		http.Error(w, "Invalid person_id", http.StatusBadRequest)
		return
	}

	upvote := r.FormValue("vote") == "up"
	comment := r.FormValue("comment")

	if _, err := db.Exec(
		"INSERT INTO votes (person_id, upvote, comment) VALUES ($1, $2, $3)",
		personID, upvote, comment,
	); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// Return simple HTML with comments for a person
func commentsHandler(w http.ResponseWriter, r *http.Request) {
	personID, err := strconv.Atoi(r.URL.Query().Get("person_id"))
	if err != nil || personID <= 0 {
		http.Error(w, "Invalid person_id", http.StatusBadRequest)
		return
	}

	rows, err := db.Query("SELECT upvote, comment FROM votes WHERE person_id = $1 ORDER BY id DESC", personID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type Comment struct {
		IsUpvote bool
		Text     string
	}
	var list []Comment
	for rows.Next() {
		var c Comment
		if err := rows.Scan(&c.IsUpvote, &c.Text); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		list = append(list, c)
	}

	// Minimal inline template to match the modal usage
	const tmpl = `
		<div>
			{{if .}}
				{{range .}}
					<p>{{if .IsUpvote}}<span style="color:green">👍</span>{{else}}<span style="color:red">👎</span>{{end}} {{.Text}}</p>
				{{end}}
			{{else}}
				<p>No comments yet.</p>
			{{end}}
		</div>`
	if err := template.Must(template.New("comments").Parse(tmpl)).Execute(w, list); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

// Helper to read current sort order from settings (defaults to "name")
func getSortOrder() string {
	var order string
	_ = db.QueryRow("SELECT value FROM settings WHERE key='sort_order'").Scan(&order)
	if order == "" {
		order = "name"
	}
	return order
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
	type Person struct {
		ID      int
		Name    string
		Score   int // upvotes - downvotes
		Upvotes int // number of positive votes
	}

	sortOrder := getSortOrder()

	// Whitelist ORDER BY to avoid injection
	orderByClause := "p.name"
	switch sortOrder {
	case "score_desc":
		orderByClause = "score DESC, p.name"
	case "upvotes_desc":
		orderByClause = "upvotes DESC, p.name"
	case "name":
		orderByClause = "p.name"
	}

	// Correctly treat NULL vote rows as 0 (not -1)
	query := `
        SELECT p.id,
               p.name,
               COALESCE(SUM(
                   CASE
                     WHEN v.upvote IS TRUE  THEN 1
                     WHEN v.upvote IS FALSE THEN -1
                     ELSE 0
                   END
               ), 0) AS score,
               COALESCE(SUM(
                   CASE
                     WHEN v.upvote IS TRUE THEN 1
                     ELSE 0
                   END
               ), 0) AS upvotes
        FROM people p
        LEFT JOIN votes v ON p.id = v.person_id
        GROUP BY p.id, p.name
        ORDER BY ` + orderByClause

	rows, err := db.Query(query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var people []Person
	for rows.Next() {
		var p Person
		if err := rows.Scan(&p.ID, &p.Name, &p.Score, &p.Upvotes); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		people = append(people, p)
	}

	tmpl := template.Must(template.ParseFiles("templates/index.html"))
	if err := tmpl.Execute(w, people); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func createTables() {
	_, err := db.Exec(`
    CREATE TABLE IF NOT EXISTS people (
        id SERIAL PRIMARY KEY,
        name TEXT NOT NULL,
        image BYTEA
    );
    CREATE TABLE IF NOT EXISTS votes (
        id SERIAL PRIMARY KEY,
        person_id INTEGER REFERENCES people(id) ON DELETE CASCADE,
        upvote BOOLEAN,
        comment TEXT
    );
    `)
	if err != nil {
		log.Fatal(err)
	}

	_, err = db.Exec(`
    CREATE TABLE IF NOT EXISTS settings (
        key TEXT PRIMARY KEY,
        value TEXT NOT NULL
    );
    `)
	if err != nil {
		log.Fatal(err)
	}

	_, err = db.Exec(`
    INSERT INTO settings (key, value)
    VALUES ('sort_order', 'name')
    ON CONFLICT (key) DO NOTHING;
    `)
	if err != nil {
		log.Fatal(err)
	}
}

func adminHandler(w http.ResponseWriter, r *http.Request) {
	pass := r.URL.Query().Get("pass")
	if pass != adminPassword {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	tmpl := template.Must(template.ParseFiles("templates/admin.html"))
	data := map[string]string{
		"AdminPass": pass,
	}
	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// Admin upload: normalize JPEGs to 512x512 (respect EXIF orientation).
// Non-JPEGs: store bytes exactly as uploaded.
func adminAddHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	pass := r.FormValue("pass")
	if pass != adminPassword {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	name := r.FormValue("name")
	file, _, err := r.FormFile("image")
	if err != nil {
		http.Error(w, "Image upload failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Read all bytes
	buf := bytes.NewBuffer(nil)
	if _, err := io.Copy(buf, file); err != nil {
		http.Error(w, "Failed to read image", http.StatusInternalServerError)
		return
	}
	imgBytes := buf.Bytes()

	// Detect format quickly
	_, format, cfgErr := image.DecodeConfig(bytes.NewReader(imgBytes))
	if cfgErr != nil {
		// If unknown, just store as-is (safer fallback)
		if _, err := db.Exec("INSERT INTO people (name, image) VALUES ($1, $2)", name, imgBytes); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	if format == "jpeg" || format == "jpg" {
		processed, err := processJPEGForDB(imgBytes, 512, 512)
		if err != nil {
			http.Error(w, "Failed to process image: "+err.Error(), http.StatusInternalServerError)
			return
		}
		_, err = db.Exec("INSERT INTO people (name, image) VALUES ($1, $2)", name, processed)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		// For non-JPEG images, store exactly as uploaded
		_, err = db.Exec("INSERT INTO people (name, image) VALUES ($1, $2)", name, imgBytes)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// Reverted: serve images exactly as stored, no processing
func imageHandler(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Path[len("/images/"):]
	id, _ := strconv.Atoi(idStr)

	var img []byte
	err := db.QueryRow("SELECT image FROM people WHERE id=$1", id).Scan(&img)
	if err != nil {
		http.Error(w, "Image not found", http.StatusNotFound)
		return
	}

	// Best-effort content-type sniff
	ct := "application/octet-stream"
	if len(img) >= 512 {
		ct = http.DetectContentType(img[:512])
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(img)
}

// Read EXIF orientation, rotate/flip, resize to fit within maxW x maxH, encode JPEG (quality 80).
func processJPEGForDB(srcBytes []byte, maxW, maxH int) ([]byte, error) {
	orientation := 1
	if ex, err := exif.Decode(bytes.NewReader(srcBytes)); err == nil {
		if tag, err := ex.Get(exif.Orientation); err == nil && tag != nil {
			if v, err := tag.Int(0); err == nil && v >= 1 && v <= 8 {
				orientation = v
			}
		}
	}

	srcImg, _, err := image.Decode(bytes.NewReader(srcBytes))
	if err != nil {
		return nil, err
	}

	// Apply EXIF orientation
	srcImg = applyEXIFOrientation(srcImg, orientation)

	w := srcImg.Bounds().Dx()
	h := srcImg.Bounds().Dy()
	dstW, dstH := fitWithin(w, h, maxW, maxH) // no upscaling

	// If already within bounds, still re-encode to strip large metadata and normalize
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), srcImg, srcImg.Bounds(), draw.Over, nil)

	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Orientation handling utilities
func applyEXIFOrientation(img image.Image, orientation int) image.Image {
	switch orientation {
	case 1:
		return img
	case 2:
		return flipHorizontal(img)
	case 3:
		return rotate180(img)
	case 4:
		return flipVertical(img)
	case 5:
		return rotate90CW(flipHorizontal(img))
	case 6:
		return rotate90CW(img)
	case 7:
		return rotate270CW(flipHorizontal(img))
	case 8:
		return rotate270CW(img)
	default:
		return img
	}
}

func flipHorizontal(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dst.Set(b.Max.X-1-(x-b.Min.X), y-b.Min.Y, src.At(x, y))
		}
	}
	return dst
}

func flipVertical(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dst.Set(x-b.Min.X, b.Max.Y-1-(y-b.Min.Y), src.At(x, y))
		}
	}
	return dst
}

func rotate180(src image.Image) image.Image {
	return rotate90CW(rotate90CW(src))
}

func rotate90CW(src image.Image) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, h, w))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(h-1-y, x, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

func rotate270CW(src image.Image) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, h, w))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(y, w-1-x, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

// Keep aspect ratio and fit within bounds. Never upscale.
func fitWithin(w, h, maxW, maxH int) (int, int) {
	if w <= 0 || h <= 0 {
		return maxW, maxH
	}
	rw := float64(maxW) / float64(w)
	rh := float64(maxH) / float64(h)
	scale := rw
	if rh < rw {
		scale = rh
	}
	if scale > 1 {
		return w, h // no upscaling
	}
	return int(float64(w) * scale), int(float64(h) * scale)
}
