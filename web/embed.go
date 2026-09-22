package web

import (
	"embed"
	"net/http"

	"github.com/go-chi/chi/v5"
)

//go:embed index.html app.js style.css favicon.svg vendor
var content embed.FS

var indexHTML, _ = content.ReadFile("index.html")

func RegisterRoutes(r chi.Router) {
	assets := http.FileServer(http.FS(content))
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})
	r.Get("/app.js", assets.ServeHTTP)
	r.Get("/style.css", assets.ServeHTTP)
	r.Get("/favicon.svg", assets.ServeHTTP)
	r.Get("/vendor/*", assets.ServeHTTP)
}
