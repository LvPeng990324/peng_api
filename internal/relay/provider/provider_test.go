package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pengapi/internal/store"
)

func testClient() *http.Client { return &http.Client{} }

func testChannel(baseURL string) store.Channel {
	return store.Channel{ID: 1, Name: "fake", Type: "openai", BaseURL: baseURL, APIKey: "upkey"}
}

func TestOpenAIChatNonStreamSuccess(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x","choices":[{"message":{"content":"hi"}}]}`)
	}))
	defer srv.Close()

	p := NewOpenAI(testClient(), 5*time.Second, time.Second)
	res := p.Chat(context.Background(), testChannel(srv.URL), ChatRequest{
		Model: "glm-5.3", Body: []byte(`{"model":"glm-5.3","messages":[]}`),
	})
	if res.Err != nil {
		t.Fatalf("Chat: %v", res.Err)
	}
	if res.HTTPStatus != 200 || !json.Valid(res.Body) {
		t.Errorf("unexpected result: status=%d body=%s", res.HTTPStatus, res.Body)
	}
	if gotAuth != "Bearer upkey" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotBody != `{"model":"glm-5.3","messages":[]}` {
		t.Errorf("body not passed through: %s", gotBody)
	}
}

func TestOpenAIChatNonStreamUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		io.WriteString(w, `{"error":{"message":"server boom"}}`)
	}))
	defer srv.Close()

	p := NewOpenAI(testClient(), 5*time.Second, time.Second)
	res := p.Chat(context.Background(), testChannel(srv.URL), ChatRequest{Model: "m", Body: []byte(`{}`)})
	if res.Err != nil {
		t.Fatalf("HTTP 500 is not a transport error: %v", res.Err)
	}
	if res.HTTPStatus != 500 || string(res.Body) != `{"error":{"message":"server boom"}}` {
		t.Errorf("status/body not preserved: %d %s", res.HTTPStatus, res.Body)
	}
}

func TestOpenAIChatConnectError(t *testing.T) {
	p := NewOpenAI(testClient(), time.Second, time.Second)
	res := p.Chat(context.Background(), testChannel("http://127.0.0.1:1"), ChatRequest{Model: "m", Body: []byte(`{}`)})
	if res.Err == nil {
		t.Fatal("connection refused must produce Err")
	}
	if res.HTTPStatus != 0 {
		t.Errorf("HTTPStatus should be 0 on transport error, got %d", res.HTTPStatus)
	}
}

func TestOpenAIListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer upkey" {
			t.Error("missing auth header")
		}
		io.WriteString(w, `{"object":"list","data":[
			{"id":"glm-5.3","object":"model"},
			{"id":"kimi-k2","object":"model","context_length":262144,"max_output_tokens":16384}
		]}`)
	}))
	defer srv.Close()

	p := NewOpenAI(testClient(), 5*time.Second, time.Second)
	models, err := p.ListModels(context.Background(), testChannel(srv.URL))
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 || models[0].ID != "glm-5.3" {
		t.Fatalf("unexpected models: %+v", models)
	}
	if models[0].ContextLength != nil {
		t.Errorf("plain openai response should have nil context_length: %+v", models[0])
	}
	if models[1].ContextLength == nil || *models[1].ContextLength != 262144 {
		t.Errorf("kimi-style context_length not parsed: %+v", models[1])
	}
}

func TestOpenAIListModelsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()
	p := NewOpenAI(testClient(), 5*time.Second, time.Second)
	_, err := p.ListModels(context.Background(), testChannel(srv.URL))
	if err == nil {
		t.Fatal("401 must return error")
	}
}

func TestOpenAIChatStreamLargeErrorBody(t *testing.T) {
	big := `{"error":{"message":"` + strings.Repeat("x", 200*1024) + `"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		io.WriteString(w, big)
	}))
	defer srv.Close()

	p := NewOpenAI(testClient(), 5*time.Second, 5*time.Second)
	res := p.Chat(context.Background(), testChannel(srv.URL), ChatRequest{
		Model: "m", Body: []byte(`{}`), Stream: true,
	})
	if res.Err != nil {
		t.Fatalf("HTTP 500 is not a transport error: %v", res.Err)
	}
	if res.Stream != nil {
		t.Error("stream must be nil on non-2xx")
	}
	if res.HTTPStatus != 500 {
		t.Errorf("status = %d", res.HTTPStatus)
	}
	if len(res.Body) != len(big) {
		t.Errorf("error body truncated: got %d bytes, want %d", len(res.Body), len(big))
	}
}

func TestRegistry(t *testing.T) {
	reg := NewRegistry(NewOpenAI(testClient(), time.Second, time.Second))
	p, ok := reg.For("openai")
	if !ok || p.Name() != "openai" {
		t.Fatal("openai provider must be registered")
	}
	if _, ok := reg.For("anthropic"); ok {
		t.Fatal("anthropic must not exist yet")
	}
}

func TestOpenAIChatStreamSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Error("stream request must set Accept")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for _, chunk := range []string{
			`data: {"choices":[{"delta":{"content":"你"}}]}` + "\n\n",
			`data: {"choices":[{"delta":{"content":"好"}}]}` + "\n\n",
			"data: [DONE]\n\n",
		} {
			io.WriteString(w, chunk)
			fl.Flush()
		}
	}))
	defer srv.Close()

	p := NewOpenAI(testClient(), 5*time.Second, 5*time.Second)
	res := p.Chat(context.Background(), testChannel(srv.URL),
		ChatRequest{Model: "m", Body: []byte(`{"stream":true}`), Stream: true})
	if res.Err != nil {
		t.Fatalf("Chat stream: %v", res.Err)
	}
	if res.Stream == nil {
		t.Fatal("Stream must be non-nil for 2xx stream")
	}
	defer res.Stream.Close()
	body, _ := io.ReadAll(res.Stream)
	if !strings.Contains(string(body), `"content":"你"`) || !strings.Contains(string(body), "[DONE]") {
		t.Errorf("stream body incomplete: %q", body)
	}
}

func TestOpenAIChatStreamHTTPErrorReadsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"message":"rate limited"}}`)
	}))
	defer srv.Close()

	p := NewOpenAI(testClient(), 5*time.Second, 5*time.Second)
	res := p.Chat(context.Background(), testChannel(srv.URL),
		ChatRequest{Model: "m", Body: []byte(`{"stream":true}`), Stream: true})
	if res.Err != nil {
		t.Fatalf("429 is HTTP-layer, not Err: %v", res.Err)
	}
	if res.HTTPStatus != 429 || !strings.Contains(string(res.Body), "rate limited") {
		t.Errorf("error body should be captured: %d %s", res.HTTPStatus, res.Body)
	}
	if res.Stream != nil {
		t.Error("Stream must be nil on HTTP error")
	}
}

func TestOpenAIChatStreamFirstByteTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second) // 不返回响应头
	}))
	defer srv.Close()

	p := NewOpenAI(testClient(), 5*time.Second, 100*time.Millisecond)
	start := time.Now()
	res := p.Chat(context.Background(), testChannel(srv.URL),
		ChatRequest{Model: "m", Body: []byte(`{"stream":true}`), Stream: true})
	if !errors.Is(res.Err, ErrStreamFirstByteTimeout) {
		t.Fatalf("expected ErrStreamFirstByteTimeout, got %v", res.Err)
	}
	if time.Since(start) > time.Second {
		t.Error("timeout should fire quickly")
	}
}

func TestOpenAIChatStreamClientCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	p := NewOpenAI(testClient(), 5*time.Second, 5*time.Second)
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	res := p.Chat(ctx, testChannel(srv.URL),
		ChatRequest{Model: "m", Body: []byte(`{"stream":true}`), Stream: true})
	if res.Err == nil {
		t.Fatal("cancelled ctx must produce Err")
	}
}
