package main

import (
	"bytes"
	"database/sql"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"

	_ "github.com/lib/pq"
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
	// Optionally pass current sort order if you update the template to show it.
	tmpl.Execute(w, nil)
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

	var img []byte
	err := db.QueryRow("SELECT image FROM people WHERE id=$1", id).Scan(&img)
	if err != nil {
		http.Error(w, "Image not found", 404)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Write(img)
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

	query := `
        SELECT p.id,
               p.name,
               COALESCE(SUM(CASE WHEN v.upvote THEN 1 ELSE -1 END), 0) AS score,
               COALESCE(SUM(CASE WHEN v.upvote THEN 1 ELSE 0 END), 0)   AS upvotes
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
