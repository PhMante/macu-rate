package main

import (
    "bytes"
    "database/sql"
    "fmt"
    "html/template"
    "io"
    "log"
    "net/http"
    "os"
    "strconv"

    _ "github.com/lib/pq"
)

var db *sql.DB
var adminPassword = "MacuRateAdmin2025"

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

    // Create tables
    createTables()

    // Routes
    http.HandleFunc("/", homeHandler)
    http.HandleFunc("/admin", adminHandler)
    http.HandleFunc("/admin/add", adminAddHandler)
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
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
    rows, err := db.Query("SELECT id, name FROM people")
    if err != nil {
        http.Error(w, err.Error(), 500)
        return
    }
    defer rows.Close()

    type Person struct {
        ID   int
        Name string
    }
    var people []Person
    for rows.Next() {
        var p Person
        rows.Scan(&p.ID, &p.Name)
        people = append(people, p)
    }

    tmpl := template.Must(template.ParseFiles("templates/index.html"))
    tmpl.Execute(w, people)
}

func adminHandler(w http.ResponseWriter, r *http.Request) {
    pass := r.URL.Query().Get("pass")
    if pass != adminPassword {
        http.Error(w, "Unauthorized", http.StatusUnauthorized)
        return
    }
    tmpl := template.Must(template.ParseFiles("templates/admin.html"))
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

