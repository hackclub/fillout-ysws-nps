package main

import (
	"embed"
	"html/template"
	"net/http"
	"time"
)

//go:embed templates/*.html
var templatesFS embed.FS

var templates = template.Must(template.ParseFS(templatesFS, "templates/*.html"))

// homeData is the view model passed to the home page template.
type homeData struct {
	Title string
	Year  int
}

// newRouter wires up the application's HTTP routes and returns a handler.
func newRouter() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", handleHome)
	mux.HandleFunc("GET /healthz", handleHealthz)
	return mux
}

func handleHome(w http.ResponseWriter, r *http.Request) {
	// GET "/" matches every unrouted path; only serve the real root.
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	data := homeData{
		Title: "Fillout YSWS NPS",
		Year:  time.Now().Year(),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ExecuteTemplate(w, "index.html", data); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}
