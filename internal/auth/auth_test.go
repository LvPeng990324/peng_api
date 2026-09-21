package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pengapi/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestBearer(t *testing.T) {
	st := openStore(t)
	tok, _ := st.CreateToken(context.Background(), "cline")

	var gotName string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotName = TokenFrom(r.Context()).Name
		io.WriteString(w, "ok")
	})
	h := Bearer(st)(inner)

	cases := []struct {
		name       string
		header     string
		wantStatus int
		wantName   string
	}{
		{"valid", "Bearer " + tok.Token, 200, "cline"},
		{"missing header", "", 401, ""},
		{"wrong scheme", "Basic abc", 401, ""},
		{"bad token", "Bearer sk-nope", 401, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotName = ""
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != c.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, c.wantStatus)
			}
			if gotName != c.wantName {
				t.Errorf("token name = %q, want %q", gotName, c.wantName)
			}
			if c.wantStatus == 401 && !strings.Contains(rec.Body.String(), `"error"`) {
				t.Errorf("401 body should be OpenAI-style error: %s", rec.Body.String())
			}
		})
	}

	// 禁用后的 token 拒绝
	st.UpdateToken(context.Background(), tok.ID, "cline", false)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Errorf("disabled token should be rejected: %d", rec.Code)
	}
}

func TestSessionStore(t *testing.T) {
	ss := NewSessionStore(50 * time.Millisecond)
	tok := ss.Create()
	if tok == "" {
		t.Fatal("empty token")
	}
	if !ss.Validate(tok) {
		t.Fatal("fresh token must validate")
	}
	if ss.Validate("bogus") {
		t.Error("bogus token must not validate")
	}
	time.Sleep(60 * time.Millisecond)
	if ss.Validate(tok) {
		t.Error("expired token must not validate")
	}
	tok2 := ss.Create()
	ss.Delete(tok2)
	if ss.Validate(tok2) {
		t.Error("deleted token must not validate")
	}
}

func TestRequireSession(t *testing.T) {
	ss := NewSessionStore(time.Hour)
	tok := ss.Create()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })
	h := RequireSession(ss)(inner)

	// 无 cookie → 401
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/me", nil))
	if rec.Code != 401 {
		t.Errorf("no cookie: %d", rec.Code)
	}
	// 有 cookie → 200
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: tok})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("with cookie: %d", rec.Code)
	}
}
