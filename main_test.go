package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pengapi/internal/admin"
	"pengapi/internal/auth"
	"pengapi/internal/config"
	"pengapi/internal/relay"
	"pengapi/internal/relay/provider"
	"pengapi/internal/store"
)

func TestEndToEnd(t *testing.T) {
	// 假上游
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","choices":[{"message":{"role":"assistant","content":"pong"}}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`)
	}))
	defer upstream.Close()

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	cfg := config.Config{
		Addr: ":0", AdminPassword: "pw", LogRetentionDays: 30, FailThreshold: 2,
		RequestTimeout: 10 * time.Second, ConnectTimeout: 2 * time.Second,
		StreamFirstByteTimeout: 2 * time.Second,
	}
	reg := provider.NewRegistry(provider.NewOpenAI(&http.Client{}, cfg.RequestTimeout, cfg.StreamFirstByteTimeout))
	engine := relay.NewEngine(st, reg, cfg.FailThreshold)
	sessions := auth.NewSessionStore(time.Hour)
	adminH := admin.New(st, sessions, cfg.AdminPassword, reg)

	srv := httptest.NewServer(buildHandler(cfg, st, engine, adminH))
	defer srv.Close()
	ctx := context.Background()

	// 数据准备
	cl := int64(131072)
	m, _ := st.CreateModel(ctx, "glm-5.3", []string{"GLM5.3"}, &cl, nil)
	ch, _ := st.CreateChannel(ctx, store.Channel{
		Name: "up", Type: "openai", BaseURL: upstream.URL, APIKey: "k", Priority: 1,
	})
	st.BindModels(ctx, ch.ID, []store.BindingInput{{UpstreamModel: "", ModelID: &m.ID}})
	tok, _ := st.CreateToken(ctx, "e2e")

	// 1. 客户端：POST /v1/chat/completions（用别名）
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"GLM5.3","messages":[{"role":"user","content":"ping"}]}`))
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	body := readAll(t, resp)
	if resp.StatusCode != 200 || !strings.Contains(body, "pong") {
		t.Fatalf("chat: %d %s", resp.StatusCode, body)
	}

	// 2. 客户端：GET /v1/models（带 context_length）
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	resp, _ = http.DefaultClient.Do(req)
	body = readAll(t, resp)
	if resp.StatusCode != 200 || !strings.Contains(body, `"glm-5.3"`) || !strings.Contains(body, `"context_length":131072`) {
		t.Fatalf("models: %d %s", resp.StatusCode, body)
	}

	// 3. 管理端：登录 → 查日志
	lreq, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/login", strings.NewReader(`{"password":"pw"}`))
	lreq.Header.Set("Content-Type", "application/json")
	lresp, _ := http.DefaultClient.Do(lreq)
	var cookie *http.Cookie
	for _, c := range lresp.Cookies() {
		if c.Name == auth.SessionCookie {
			cookie = c
		}
	}
	lresp.Body.Close()
	if cookie == nil {
		t.Fatal("no session cookie")
	}
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/logs", nil)
	req.AddCookie(cookie)
	resp, _ = http.DefaultClient.Do(req)
	var logsPage struct {
		Total int `json:"total"`
	}
	json.NewDecoder(resp.Body).Decode(&logsPage)
	resp.Body.Close()
	if logsPage.Total != 1 {
		t.Fatalf("expected 1 log, got %d", logsPage.Total)
	}

	// 4. SPA 静态资源
	resp, _ = http.Get(srv.URL + "/")
	body = readAll(t, resp)
	if resp.StatusCode != 200 || !strings.Contains(body, `id="app"`) {
		t.Fatalf("index: %d", resp.StatusCode)
	}
	resp, _ = http.Get(srv.URL + "/vendor/vue.global.prod.js")
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("vue vendor: %d", resp.StatusCode)
	}
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}
