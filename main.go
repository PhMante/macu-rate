package main

import (
	"database/sql"
	"embed"
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed templates/*
var templatesFS embed.FS

var tmpl = template.Must(template.ParseFS(templatesFS, "templates/*.html"))

// Person is used for both HTML templates and JSON API
type Person struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Photo string `json:"photo"`
	Score int    `json:"score"`
}

// Comment for HTML fragment or API
type Comment struct {
	ID       int    `json:"id"`
	PersonID int    `json:"person_id"`
	Text     string `json:"text"`
	Upvote   bool   `json:"upvote"`
	Created  string `json:"created_at"`
}

var db *sql.DB

// default admin password; override by setting ADMIN_PASSWORD env var
const defaultAdminPassword = "macurate2025"

func main() {
	// allow overriding admin password via env var
	if os.Getenv("ADMIN_PASSWORD") == "" {
		os.Setenv("ADMIN_PASSWORD", defaultAdminPassword)
	}

	var err error
	db, err = sql.Open("sqlite", "macurate.db")
	if err != nil {
		log.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	if err := createTables(); err != nil {
		log.Fatalf("createTables: %v", err)
	}

	// static files (optional images)
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	// server-rendered pages
	http.HandleFunc("/", homeHandler)         // GET: server-rendered homepage
	http.HandleFunc("/comments", commentsHandler) // returns HTML fragment (server-side)

	// admin
	http.HandleFunc("/admin", adminHandler)       // GET show login, POST login
	http.HandleFunc("/admin/add", adminAddHandler) // GET show add, POST add
	http.HandleFunc("/logout", logoutHandler)

	// API for static frontend
	http.HandleFunc("/api/people", apiPeopleHandler)   // GET -> JSON list
	http.HandleFunc("/api/comments", apiCommentsHandler) // GET -> JSON comments
	http.HandleFunc("/api/vote", apiVoteHandler)       // POST -> JSON (vote + comment)

	// Also keep legacy form-based /vote for server templates
	http.HandleFunc("/vote", voteHandler)

	// listen
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("listening on :%s (admin password set)", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func createTables() error {
	_, err := db.Exec(`
	CREATE TABLE IF NOT EXISTS people (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		photo TEXT NOT NULL,
		score INTEGER NOT NULL DEFAULT 0
	);
	`)
	if err != nil {
		return err
	}

	_, err = db.Exec(`
	CREATE TABLE IF NOT EXISTS comments (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		person_id INTEGER NOT NULL,
		text TEXT NOT NULL,
		is_upvote INTEGER NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(person_id) REFERENCES people(id) ON DELETE CASCADE
	);
	`)
	return err
}

// =========================
// Helper: set simple CORS for API
func setCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*") // change in production if needed
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

// =========================
// server-side homepage (renders template)
func homeHandler(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query("SELECT id, name, photo, score FROM people ORDER BY score DESC, id ASC")
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		log.Printf("homeHandler query: %v", err)
		return
	}
	defer rows.Close()

	var people []Person
	for rows.Next() {
		var p Person
		if err := rows.Scan(&p.ID, &p.Name, &p.Photo, &p.Score); err != nil {
			http.Error(w, "db scan error", http.StatusInternalServerError)
			log.Printf("homeHandler scan: %v", err)
			return
		}
		people = append(people, p)
	}
	if err := tmpl.ExecuteTemplate(w, "home.html", people); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		log.Printf("homeHandler exec: %v", err)
		return
	}
}

// =========================
// server-side comments fragment (HTML)
func commentsHandler(w http.ResponseWriter, r *http.Request) {
	// expects ?id=NN
	idStr := strings.TrimSpace(r.URL.Query().Get("id"))
	if idStr == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	id, err := strconv.Atoi(idStr)
	if err != nil || id <= 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	rows, err := db.Query("SELECT id, text, is_upvote, created_at FROM comments WHERE person_id = ? ORDER BY created_at DESC", id)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		log.Printf("commentsHandler query: %v", err)
		return
	}
	defer rows.Close()

	var list []Comment
	for rows.Next() {
		var c Comment
		var isUp int
		if err := rows.Scan(&c.ID, &c.Text, &isUp, &c.Created); err != nil {
			http.Error(w, "db scan error", http.StatusInternalServerError)
			log.Printf("commentsHandler scan: %v", err)
			return
		}
		c.Upvote = isUp != 0
		list = append(list, c)
	}

	if err := tmpl.ExecuteTemplate(w, "comments.html", list); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		log.Printf("commentsHandler exec: %v", err)
		return
	}
}

// =========================
// API: JSON list of people
func apiPeopleHandler(w http.ResponseWriter, r *http.Request) {
	// CORS preflight
	if r.Method == http.MethodOptions {
		setCORS(w)
		w.WriteHeader(http.StatusOK)
		return
	}
	setCORS(w)

	rows, err := db.Query("SELECT id, name, photo, score FROM people ORDER BY score DESC, id ASC")
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		log.Printf("apiPeopleHandler query: %v", err)
		return
	}
	defer rows.Close()

	var people []Person
	for rows.Next() {
		var p Person
		if err := rows.Scan(&p.ID, &p.Name, &p.Photo, &p.Score); err != nil {
			http.Error(w, "db scan error", http.StatusInternalServerError)
			log.Printf("apiPeopleHandler scan: %v", err)
			return
		}
		people = append(people, p)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(people)
}

// =========================
// API: comments (JSON)
func apiCommentsHandler(w http.ResponseWriter, r *http.Request) {
	// CORS
	if r.Method == http.MethodOptions {
		setCORS(w)
		w.WriteHeader(http.StatusOK)
		return
	}
	setCORS(w)

	idStr := strings.TrimSpace(r.URL.Query().Get("id"))
	if idStr == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	id, err := strconv.Atoi(idStr)
	if err != nil || id <= 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	rows, err := db.Query("SELECT id, person_id, text, is_upvote, created_at FROM comments WHERE person_id = ? ORDER BY created_at DESC", id)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		log.Printf("apiCommentsHandler query: %v", err)
		return
	}
	defer rows.Close()

	var out []Comment
	for rows.Next() {
		var c Comment
		var isUp int
		if err := rows.Scan(&c.ID, &c.PersonID, &c.Text, &isUp, &c.Created); err != nil {
			http.Error(w, "db scan error", http.StatusInternalServerError)
			log.Printf("apiCommentsHandler scan: %v", err)
			return
		}
		c.Upvote = isUp != 0
		out = append(out, c)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// =========================
// processVote - helper used by both API and form flow
func processVote(id int, direction string, comment string) (newScore int, err error) {
	var delta int
	var isUp int
	if direction == "up" {
		delta = 1
		isUp = 1
	} else if direction == "down" {
		delta = -1
		isUp = 0
	} else {
		return 0, http.ErrNotSupported
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}

	res, err := tx.Exec("UPDATE people SET score = score + ? WHERE id = ?", delta, id)
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	ra, _ := res.RowsAffected()
	if ra == 0 {
		tx.Rollback()
		return 0, sql.ErrNoRows
	}

	_, err = tx.Exec("INSERT INTO comments (person_id, text, is_upvote, created_at) VALUES (?, ?, ?, ?)",
		id, comment, isUp, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		tx.Rollback()
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}

	// fetch new score
	var score int
	err = db.QueryRow("SELECT score FROM people WHERE id = ?", id).Scan(&score)
	if err != nil {
		return 0, err
	}
	return score, nil
}

// =========================
// API vote endpoint (used by static frontend)
func apiVoteHandler(w http.ResponseWriter, r *http.Request) {
	// CORS preflight
	if r.Method == http.MethodOptions {
		setCORS(w)
		w.WriteHeader(http.StatusOK)
		return
	}
	setCORS(w)

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// accept application/json or form-data
	contentType := r.Header.Get("Content-Type")
	var id int
	var direction string
	var comment string

	if strings.HasPrefix(contentType, "application/json") {
		var payload struct {
			ID        int    `json:"id"`
			Direction string `json:"direction"`
			Comment   string `json:"comment"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		id = payload.ID
		direction = payload.Direction
		comment = strings.TrimSpace(payload.Comment)
	} else {
		// form
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		idStr := strings.TrimSpace(r.PostFormValue("id"))
		direction = strings.TrimSpace(r.PostFormValue("direction"))
		comment = strings.TrimSpace(r.PostFormValue("comment"))
		var err error
		id, err = strconv.Atoi(idStr)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
	}

	if id <= 0 || (direction != "up" && direction != "down") || comment == "" {
		http.Error(w, "missing or invalid fields", http.StatusBadRequest)
		return
	}

	newScore, err := processVote(id, direction, comment)
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "person not found", http.StatusNotFound)
			return
		}
		http.Error(w, "db error", http.StatusInternalServerError)
		log.Printf("apiVoteHandler processVote: %v", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"ok":    true,
		"score": newScore,
	})
}

// =========================
// legacy form-based vote for server-rendered homepage
func voteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	id, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("id")))
	if err != nil || id <= 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	direction := strings.TrimSpace(r.PostFormValue("direction"))
	comment := strings.TrimSpace(r.PostFormValue("comment"))
	if comment == "" || (direction != "up" && direction != "down") {
		http.Error(w, "missing fields", http.StatusBadRequest)
		return
	}
	_, err = processVote(id, direction, comment)
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "person not found", http.StatusNotFound)
			return
		}
		http.Error(w, "db error", http.StatusInternalServerError)
		log.Printf("voteHandler: processVote: %v", err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// =========================
// Admin handlers (simple single account)
func adminHandler(w http.ResponseWriter, r *http.Request) {
	// GET -> show login
	// POST -> attempt login
	if r.Method == http.MethodGet {
		// template accepts optional .Error
		tmpl.ExecuteTemplate(w, "admin.html", nil)
		return
	}
	// POST login
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	pass := r.PostFormValue("password")
	expected := os.Getenv("ADMIN_PASSWORD")
	if expected == "" {
		expected = defaultAdminPassword
	}
	if pass != expected {
		tmpl.ExecuteTemplate(w, "admin.html", struct{ Error string }{Error: "invalid password"})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "macu_admin",
		Value:    "1",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   60 * 60 * 24 * 7,
	})
	http.Redirect(w, r, "/admin/add", http.StatusSeeOther)
}

func adminAddHandler(w http.ResponseWriter, r *http.Request) {
	// check cookie
	c, err := r.Cookie("macu_admin")
	if err != nil || c.Value != "1" {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodGet {
		// show form
		tmpl.ExecuteTemplate(w, "add.html", nil)
		return
	}
	// POST -> add person
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	photo := strings.TrimSpace(r.PostFormValue("photo"))
	if name == "" || photo == "" {
		http.Error(w, "missing fields", http.StatusBadRequest)
		return
	}
	_, err = db.Exec("INSERT INTO people (name, photo) VALUES (?, ?)", name, photo)
	if err != nil {
		http.Error(w, "db insert error", http.StatusInternalServerError)
		log.Printf("adminAddHandler insert: %v", err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func logoutHandler(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:   "macu_admin",
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

