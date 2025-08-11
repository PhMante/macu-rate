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
	"golang.org/x/image/draw"
)

var db *sql.DB
var adminPassword string

func main() {
	// Database connection
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL environment variable not set")
	}
	var err error
	db, err = sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatal(err)
	}
	err = db.Ping()
	if err != nil {
		log.Fatal(err)
	}

	// Admin password from environment
	adminPassword = os.Getenv("ADMIN_PASSWORD")
	if adminPassword == "" {
		log.Fatal("ADMIN_PASSWORD environment variable not set")
	}

	// Create tables
	createTables()

	// Routes
	http.HandleFunc("/", homeHandler)
	http.HandleFunc("/admin", adminHandler)
	http.HandleFunc("/admin/add", adminAddHandler)
	http.HandleFunc("/admin/sort", adminSortHandler) // NEW: set global sort
	http.HandleFunc("/vote", voteHandler)
	http.HandleFunc("/comments", commentsHandler)
	http.HandleFunc("/images/", imageHandler)

	// Static
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Println("Listening on port", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
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

	// Global settings for the site (e.g., sort order)
	_, err = db.Exec(`
    CREATE TABLE IF NOT EXISTS settings (
        key TEXT PRIMARY KEY,
        value TEXT NOT NULL
    );
    `)
	if err != nil {
		log.Fatal(err)
	}

	// Ensure a default sort order exists
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
	// Provide the pass to the template so hidden inputs have the correct value
	data := map[string]string{
		"AdminPass": pass,
	}
	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func adminAddHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", 405)
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
		http.Error(w, "Image upload failed: "+err.Error(), 400)
		return
	}
	defer file.Close()
	buf := bytes.NewBuffer(nil)
	io.Copy(buf, file)

	_, err = db.Exec("INSERT INTO people (name, image) VALUES ($1, $2)", name, buf.Bytes())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func adminSortHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	pass := r.FormValue("pass")
	if pass != adminPassword {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	order := r.FormValue("order")
	// Whitelist supported orders to avoid injection
	switch order {
	case "name", "score_desc", "upvotes_desc":
		// ok
	default:
		http.Error(w, "Invalid sort order", http.StatusBadRequest)
		return
	}

	_, err := db.Exec("UPDATE settings SET value=$1 WHERE key='sort_order'", order)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	http.Redirect(w, r, "/admin?pass="+pass, http.StatusSeeOther)
}

func voteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	personID, _ := strconv.Atoi(r.FormValue("person_id"))
	upvote := r.FormValue("vote") == "up"
	comment := r.FormValue("comment")

	_, err := db.Exec("INSERT INTO votes (person_id, upvote, comment) VALUES ($1, $2, $3)",
		personID, upvote, comment)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func commentsHandler(w http.ResponseWriter, r *http.Request) {
	personID, _ := strconv.Atoi(r.URL.Query().Get("person_id"))
	rows, err := db.Query("SELECT upvote, comment FROM votes WHERE person_id=$1", personID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()

	type Comment struct {
		Upvote  bool
		Comment string
	}
	var comments []Comment
	for rows.Next() {
		var c Comment
		rows.Scan(&c.Upvote, &c.Comment)
		comments = append(comments, c)
	}
	tmpl := `
    <html><body>
    {{range .}}
        <p>{{if .Upvote}}⬆️{{else}}⬇️{{end}} {{.Comment}}</p>
    {{end}}
    </body></html>`
	template.Must(template.New("comments").Parse(tmpl)).Execute(w, comments)
}

func imageHandler(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Path[len("/images/"):]
	id, _ := strconv.Atoi(idStr)

	var imgBytes []byte
	err := db.QueryRow("SELECT image FROM people WHERE id=$1", id).Scan(&imgBytes)
	if err != nil {
		http.Error(w, "Image not found", http.StatusNotFound)
		return
	}

	// Detect image format
	cfg, format, cfgErr := image.DecodeConfig(bytes.NewReader(imgBytes))
	if cfgErr != nil {
		// If we can't decode config, try to stream as-is with sniffed content type
		ct := "application/octet-stream"
		if len(imgBytes) >= 512 {
			ct = http.DetectContentType(imgBytes[:512])
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(imgBytes)
		return
	}

	// If not JPEG, serve original bytes as-is.
	if format != "jpeg" && format != "jpg" {
		w.Header().Set("Content-Type", contentTypeFromFormat(format, imgBytes))
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(imgBytes)
		return
	}

	// JPEG: allow downscaling
	q := r.URL.Query()
	maxW, _ := strconv.Atoi(q.Get("w"))
	maxH, _ := strconv.Atoi(q.Get("h"))
	if maxW <= 0 {
		maxW = 512
	}
	if maxH <= 0 {
		maxH = 512
	}

	// If already small enough, serve original
	if cfg.Width <= maxW && cfg.Height <= maxH {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(imgBytes)
		return
	}

	// Decode and resize only for JPEG
	src, _, err := image.Decode(bytes.NewReader(imgBytes))
	if err != nil {
		http.Error(w, "Failed to decode image", http.StatusInternalServerError)
		return
	}

	dstW, dstH := fitWithin(src.Bounds().Dx(), src.Bounds().Dy(), maxW, maxH)
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 80}); err != nil {
		http.Error(w, "Failed to encode image", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(buf.Bytes())
}

// fitWithin keeps aspect ratio while fitting within maxW x maxH
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
		// Don't upscale
		return w, h
	}
	return int(float64(w) * scale), int(float64(h) * scale)
}

func contentTypeFromFormat(format string, data []byte) string {
	switch format {
	case "jpeg", "jpg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "gif":
		return "image/gif"
	default:
		// Try sniffing; fallback to jpeg if unknown
		if len(data) >= 512 {
			return http.DetectContentType(data[:512])
		}
		return "image/jpeg"
	}
}

func getSortOrder() string {
	var order string
	_ = db.QueryRow("SELECT value FROM settings WHERE key='sort_order'").Scan(&order)
	if order == "" {
		order = "name"
	}
	return order
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
	// First, get all people with their vote stats
	type Person struct {
		ID      int
		Name    string
		Score   int // net score: upvotes - downvotes
		Upvotes int // number of positive votes
	}

	sortOrder := getSortOrder()

	// Build ORDER BY safely via a whitelist
	orderByClause := "p.name"
	switch sortOrder {
	case "score_desc":
		orderByClause = "score DESC, p.name"
	case "upvotes_desc":
		orderByClause = "upvotes DESC, p.name"
	case "name":
		orderByClause = "p.name"
	}

	// IMPORTANT: Handle NULLs from the LEFT JOIN as 0, not -1
	// - Score:  +1 for true, -1 for false, 0 for NULL
	// - Upvotes: +1 for true, 0 otherwise (including NULL)
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
