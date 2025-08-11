package main

import (
	"database/sql"
	"embed"
	"html/template"
	"log"
	"net/http"
	"strconv"

	_ "modernc.org/sqlite"
)

//go:embed templates/*
var templatesFS embed.FS

var tmpl = template.Must(template.ParseFS(templatesFS, "templates/*.html"))

type Person struct {
	ID    int
	Name  string
	Photo string
	Score int
}

type Comment struct {
	Text     string
	IsUpvote bool
}

var db *sql.DB

const adminPassword = "macurate2025"

func main() {
	var err error
	db, err = sql.Open("sqlite", "macurate.db")
	if err != nil {
		log.Fatal(err)
	}

	createTables()

	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	http.HandleFunc("/", homeHandler)
	http.HandleFunc("/vote", voteHandler)
	http.HandleFunc("/comments", commentsHandler)
	http.HandleFunc("/admin", adminHandler)
	http.HandleFunc("/admin/add", adminAddHandler)
	http.HandleFunc("/logout", logoutHandler)

	log.Println("Server running on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func createTables() {
	_, err := db.Exec(`
	CREATE TABLE IF NOT EXISTS people (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT,
		photo TEXT,
		score INTEGER DEFAULT 0
	);
	CREATE TABLE IF NOT EXISTS comments (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		person_id INTEGER,
		text TEXT,
		is_upvote BOOLEAN
	);
	`)
	if err != nil {
		log.Fatal(err)
	}
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query("SELECT id, name, photo, score FROM people ORDER BY score DESC")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()

	var people []Person
	for rows.Next() {
		var p Person
		rows.Scan(&p.ID, &p.Name, &p.Photo, &p.Score)
		people = append(people, p)
	}

	tmpl.ExecuteTemplate(w, "home.html", people)
}

func voteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	id, _ := strconv.Atoi(r.FormValue("id"))
	direction := r.FormValue("direction")
	comment := r.FormValue("comment")

	if direction == "up" {
		_, _ = db.Exec("UPDATE people SET score = score + 1 WHERE id = ?", id)
		_, _ = db.Exec("INSERT INTO comments (person_id, text, is_upvote) VALUES (?, ?, 1)", id, comment)
	} else if direction == "down" {
		_, _ = db.Exec("UPDATE people SET score = score - 1 WHERE id = ?", id)
		_, _ = db.Exec("INSERT INTO comments (person_id, text, is_upvote) VALUES (?, ?, 0)", id, comment)
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func commentsHandler(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.URL.Query().Get("id"))
	rows, err := db.Query("SELECT text, is_upvote FROM comments WHERE person_id = ?", id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()

	var comments []Comment
	for rows.Next() {
		var c Comment
		rows.Scan(&c.Text, &c.IsUpvote)
		comments = append(comments, c)
	}

	tmpl.ExecuteTemplate(w, "comments.html", comments)
}

func adminHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		pass := r.FormValue("password")
		if pass == adminPassword {
			http.SetCookie(w, &http.Cookie{Name: "admin", Value: "true"})
			http.Redirect(w, r, "/admin/add", http.StatusSeeOther)
			return
		}
		http.Error(w, "Invalid password", http.StatusUnauthorized)
		return
	}
	tmpl.ExecuteTemplate(w, "admin.html", nil)
}

func adminAddHandler(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("admin")
	if err != nil || cookie.Value != "true" {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}

	if r.Method == http.MethodPost {
		name := r.FormValue("name")
		photo := r.FormValue("photo")
		_, err := db.Exec("INSERT INTO people (name, photo) VALUES (?, ?)", name, photo)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	tmpl.ExecuteTemplate(w, "add.html", nil)
}

func logoutHandler(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "admin", Value: "", MaxAge: -1})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
