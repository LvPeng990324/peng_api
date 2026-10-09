package web

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestStaticRoutes(t *testing.T) {
	// 从 embed FS 找一个 dist/assets 下的真实产物文件
	var assetPath string
	err := fs.WalkDir(content, "dist/assets", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && assetPath == "" {
			assetPath = strings.TrimPrefix(path, "dist")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk dist/assets: %v", err)
	}
	if assetPath == "" {
		t.Fatal("dist/assets 下没有产物文件，先执行 cd web && npm run build")
	}

	r := chi.NewRouter()
	RegisterRoutes(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	for _, path := range []string{"/", "/favicon.svg", assetPath, "/some/spa/route"} {
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

	// SPA fallback 不应吞掉 API 未命中路径
	resp, err := http.Get(srv.URL + "/api/nonexistent")
	if err != nil {
		t.Fatalf("GET /api/nonexistent: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("GET /api/nonexistent: status=%d, want 404", resp.StatusCode)
	}
}
