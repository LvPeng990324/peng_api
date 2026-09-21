package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestStaticRoutes(t *testing.T) {
	r := chi.NewRouter()
	RegisterRoutes(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	for _, path := range []string{"/", "/app.js", "/style.css", "/vendor/vue.global.prod.js"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || len(body) == 0 {
			t.Errorf("GET %s: status=%d len=%d", path, resp.StatusCode, len(body))
		}
		if path == "/" && !strings.Contains(string(body), `id="app"`) {
			t.Error("index.html should contain #app")
		}
	}
}
