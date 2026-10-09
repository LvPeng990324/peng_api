package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

//go:embed all:dist
var content embed.FS

// 前端构建产物目录；embed 在编译期已校验 dist 存在
var distFS = mustSub(content, "dist")

func mustSub(f embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(err) // 仅启动期（init）可能触发，请求路径不会走到
	}
	return sub
}

var indexHTML, _ = fs.ReadFile(distFS, "index.html")

func serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

func RegisterRoutes(r chi.Router) {
	assets := http.FileServer(http.FS(distFS))
	r.Get("/", serveIndex)
	r.Get("/favicon.svg", assets.ServeHTTP)
	r.Get("/assets/*", assets.ServeHTTP)
	// SPA fallback：其余未命中的 GET 返回 index.html（/api、/v1 由 chi 精确路由优先匹配）
	r.Get("/*", func(w http.ResponseWriter, req *http.Request) {
		if strings.HasPrefix(req.URL.Path, "/api/") || strings.HasPrefix(req.URL.Path, "/v1/") {
			http.NotFound(w, req)
			return
		}
		serveIndex(w, req)
	})
}
