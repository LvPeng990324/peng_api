package relay

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"pengapi/internal/auth"
	"pengapi/internal/relay/provider"
	"pengapi/internal/store"
)

func TestReplaceModel(t *testing.T) {
	out, err := replaceModel([]byte(`{"model":"GLM5.3","messages":[{"role":"user","content":"hi"}]}`), "glm-5.3-free")
	if err != nil {
		t.Fatalf("replaceModel: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, `"model":"glm-5.3-free"`) || !strings.Contains(s, `"content":"hi"`) {
		t.Errorf("unexpected: %s", s)
	}
	if _, err := replaceModel([]byte(`not json`), "m"); err == nil {
		t.Error("invalid json should error")
	}
}

func TestNewUUIDFormat(t *testing.T) {
	a, b := newUUID(), newUUID()
	if a == b {
		t.Error("uuids must differ")
	}
	if len(a) != 36 || a[8] != '-' || a[13] != '-' || a[14] != '4' {
		t.Errorf("bad uuid v4 format: %s", a)
	}
}

func TestParseUsage(t *testing.T) {
	p, c, h := parseUsage([]byte(`{"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_cache_hit_tokens":7}}`))
	if p == nil || *p != 10 || c == nil || *c != 5 {
		t.Errorf("usage: %v %v", p, c)
	}
	if h == nil || *h != 7 {
		t.Errorf("cache hit: %v", h)
	}
	p, c, h = parseUsage([]byte(`{"usage":{"prompt_tokens":10,"completion_tokens":5}}`))
	if p == nil || h != nil {
		t.Errorf("cache hit absent should be nil: %v %v", p, h)
	}
	p, c, h = parseUsage([]byte(`{"choices":[]}`))
	if p != nil || c != nil || h != nil {
		t.Errorf("no usage should return nils: %v %v %v", p, c, h)
	}
	p, c, h = parseUsage([]byte(`broken`))
	if p != nil || c != nil || h != nil {
		t.Errorf("bad json should return nils")
	}
}

func TestParseUsageCacheHitVariants(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"deepseek", `{"usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_cache_hit_tokens":7}}`, 7},
		{"kimi", `{"usage":{"prompt_tokens":10,"completion_tokens":5,"cached_tokens":6,"prompt_tokens_details":{"cached_tokens":6}}}`, 6},
		{"kimi non-stream small prompt no cache", `{"usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":0}}}`, 0},
		{"anthropic", `{"usage":{"prompt_tokens":10,"completion_tokens":5,"cache_read_input_tokens":8}}`, 8},
		{"gemini", `{"usage":{"prompt_tokens":10,"completion_tokens":5,"cached_content_token_count":9}}`, 9},
		{"openai details only", `{"usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":4}}}`, 4},
		{"priority deepseek over details", `{"usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_cache_hit_tokens":7,"cached_tokens":6,"prompt_tokens_details":{"cached_tokens":6}}}`, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, h := parseUsage([]byte(tc.body))
			if h == nil || *h != tc.want {
				t.Errorf("got %v, want %d", h, tc.want)
			}
		})
	}
	// 全缺省 → nil
	_, _, h := parseUsage([]byte(`{"usage":{"prompt_tokens":10,"completion_tokens":5}}`))
	if h != nil {
		t.Errorf("no cache fields should be nil, got %v", *h)
	}
}

func TestParseUsageSSE(t *testing.T) {
	text := "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1,\"prompt_cache_hit_tokens\":2}}\n\n" +
		"data: [DONE]\n\n"
	p, c, h := parseUsageSSE(text)
	if p == nil || *p != 3 || c == nil || *c != 1 {
		t.Errorf("sse usage: %v %v", p, c)
	}
	if h == nil || *h != 2 {
		t.Errorf("sse cache hit: %v", h)
	}
	p, c, h = parseUsageSSE("data: [DONE]\n\n")
	if p != nil || c != nil || h != nil {
		t.Errorf("no usage chunk should return nils")
	}
}

// ---------- 引擎测试辅助 ----------

type fakeUpstream struct {
	srv     *httptest.Server
	handler http.HandlerFunc
	lastReq atomic.Value // string：最近一次收到的请求体
	hits    atomic.Int64
}

func newFakeUpstream(h http.HandlerFunc) *fakeUpstream {
	f := &fakeUpstream{}
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		f.lastReq.Store(string(b))
		h(w, r)
	}
	f.srv = httptest.NewServer(f.handler)
	return f
}

func (f *fakeUpstream) close()      { f.srv.Close() }
func (f *fakeUpstream) url() string { return f.srv.URL }
func (f *fakeUpstream) lastBody() string {
	if v := f.lastReq.Load(); v != nil {
		return v.(string)
	}
	return ""
}

type testEnv struct {
	st     *store.Store
	tok    *store.Token
	client *httptest.Server
}

func newTestEnv(t *testing.T, fbTimeout time.Duration) *testEnv {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	tok, err := st.CreateToken(context.Background(), "tester")
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	reg := provider.NewRegistry(provider.NewOpenAI(&http.Client{}, 10*time.Second, fbTimeout))
	engine := NewEngine(st, reg, 3)
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(auth.Bearer(st))
		r.Post("/chat/completions", engine.ChatCompletions)
		r.Get("/models", engine.Models)
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &testEnv{st: st, tok: tok, client: srv}
}

func (e *testEnv) addChannel(t *testing.T, name, baseURL string, priority int, modelID int64, upstreamModel string) *store.Channel {
	t.Helper()
	ch, err := e.st.CreateChannel(context.Background(), store.Channel{
		Name: name, Type: "openai", BaseURL: baseURL, APIKey: "upkey", Priority: priority,
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if err := e.st.BindModels(context.Background(), ch.ID, []store.BindingInput{{
		UpstreamModel: upstreamModel, ModelID: &modelID,
	}}); err != nil {
		t.Fatalf("BindModels: %v", err)
	}
	return ch
}

func (e *testEnv) chat(t *testing.T, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.client.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+e.tok.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("chat request: %v", err)
	}
	return resp
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// ---------- 非流式引擎测试 ----------

func TestEngineSuccessAliasAndUpstreamRename(t *testing.T) {
	up := newFakeUpstream(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"pong"}}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	})
	defer up.close()

	env := newTestEnv(t, 5*time.Second)
	m, _ := env.st.CreateModel(context.Background(), "glm-5.3", []string{"GLM5.3"}, nil, nil)
	env.addChannel(t, "zhipu", up.url(), 1, m.ID, "glm-5.3-free")

	resp := env.chat(t, `{"model":"GLM5.3","messages":[{"role":"user","content":"ping"}]}`)
	body := readBody(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "pong") {
		t.Errorf("upstream body not passed through: %s", body)
	}
	// 上游收到的 model 必须是 upstream_model，不是客户端的别名
	if !strings.Contains(up.lastBody(), `"model":"glm-5.3-free"`) {
		t.Errorf("upstream got wrong model: %s", up.lastBody())
	}
	// 日志：成功一条，记录别名→标准模型、token、usage
	logs, total, err := env.st.ListLogs(context.Background(), store.LogFilter{Page: 1, Size: 10})
	if err != nil || total != 1 {
		t.Fatalf("logs: %v total=%d", err, total)
	}
	l := logs[0]
	if l.Status != "success" || l.ModelRequested != "GLM5.3" || l.ModelCanonical != "glm-5.3" ||
		l.TokenName != "tester" || l.ChannelName != "zhipu" || l.Attempt != 1 {
		t.Errorf("bad log entry: %+v", l)
	}
	if l.PromptTokens == nil || *l.PromptTokens != 3 {
		t.Errorf("usage not parsed: %+v", l)
	}
}

func TestEngineFailoverOn500(t *testing.T) {
	bad := newFakeUpstream(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		io.WriteString(w, `{"error":{"message":"boom"}}`)
	})
	defer bad.close()
	good := newFakeUpstream(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	})
	defer good.close()

	env := newTestEnv(t, 5*time.Second)
	m, _ := env.st.CreateModel(context.Background(), "m1", nil, nil, nil)
	chBad := env.addChannel(t, "bad", bad.url(), 10, m.ID, "m1") // 优先级高，先试
	env.addChannel(t, "good", good.url(), 1, m.ID, "m1")

	resp := env.chat(t, `{"model":"m1","messages":[]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("should failover to good channel: %d %s", resp.StatusCode, readBody(t, resp))
	}
	// 两次尝试、同一 request_id
	logs, total, _ := env.st.ListLogs(context.Background(), store.LogFilter{Page: 1, Size: 10})
	if total != 2 {
		t.Fatalf("expected 2 attempt logs, got %d", total)
	}
	if logs[0].RequestID != logs[1].RequestID {
		t.Error("attempts must share request_id")
	}
	var failed, success *store.LogEntry
	for i := range logs {
		if logs[i].Status == "failed" {
			failed = &logs[i]
		} else {
			success = &logs[i]
		}
	}
	if failed == nil || success == nil || failed.ChannelName != "bad" || success.Attempt != 2 {
		t.Errorf("bad attempts: %+v", logs)
	}
	// bad 计一次失败；good 无失败
	got, _ := env.st.GetChannel(context.Background(), chBad.ID)
	if got.ConsecutiveFailures != 1 {
		t.Errorf("bad channel failures = %d, want 1", got.ConsecutiveFailures)
	}
}

func TestEngine400PassthroughNoFailoverNoCount(t *testing.T) {
	up := newFakeUpstream(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"message":"bad request: max_tokens too large"}}`)
	})
	defer up.close()
	other := newFakeUpstream(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[]}`)
	})
	defer other.close()

	env := newTestEnv(t, 5*time.Second)
	m, _ := env.st.CreateModel(context.Background(), "m1", nil, nil, nil)
	ch := env.addChannel(t, "ch", up.url(), 10, m.ID, "m1")
	env.addChannel(t, "other", other.url(), 1, m.ID, "m1")

	resp := env.chat(t, `{"model":"m1","messages":[]}`)
	body := readBody(t, resp)
	if resp.StatusCode != 400 || !strings.Contains(body, "max_tokens too large") {
		t.Errorf("4xx must pass through verbatim: %d %s", resp.StatusCode, body)
	}
	if other.hits.Load() != 0 {
		t.Error("4xx must not trigger failover")
	}
	got, _ := env.st.GetChannel(context.Background(), ch.ID)
	if got.ConsecutiveFailures != 0 {
		t.Error("4xx must not count as channel failure")
	}
}

func TestEngineAllFail503AndAutoDisable(t *testing.T) {
	up := newFakeUpstream(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
	})
	defer up.close()

	env := newTestEnv(t, 5*time.Second) // failThreshold=3
	m, _ := env.st.CreateModel(context.Background(), "m1", nil, nil, nil)
	ch := env.addChannel(t, "only", up.url(), 1, m.ID, "m1")

	for i := 0; i < 3; i++ {
		resp := env.chat(t, `{"model":"m1","messages":[]}`)
		body := readBody(t, resp)
		if resp.StatusCode != 503 || !strings.Contains(body, "all channels failed") {
			t.Fatalf("attempt %d: %d %s", i, resp.StatusCode, body)
		}
	}
	got, _ := env.st.GetChannel(context.Background(), ch.ID)
	if !got.AutoDisabled || got.DisabledUntil == nil {
		t.Fatalf("channel should be auto-disabled after 3 failures: %+v", got)
	}
	// 第 4 次：唯一渠道已禁用 → 503 no available channel
	resp := env.chat(t, `{"model":"m1","messages":[]}`)
	body := readBody(t, resp)
	if resp.StatusCode != 503 || !strings.Contains(body, "no available channel") {
		t.Errorf("expected no-channel 503: %d %s", resp.StatusCode, body)
	}
}

func TestEngineUnknownModelAndBadRequests(t *testing.T) {
	env := newTestEnv(t, 5*time.Second)

	resp := env.chat(t, `{"model":"nope","messages":[]}`)
	if resp.StatusCode != 404 {
		t.Errorf("unknown model: %d", resp.StatusCode)
	}
	readBody(t, resp)

	resp = env.chat(t, `{"messages":[]}`)
	if resp.StatusCode != 400 {
		t.Errorf("missing model: %d", resp.StatusCode)
	}
	readBody(t, resp)

	resp = env.chat(t, `not-json`)
	if resp.StatusCode != 400 {
		t.Errorf("bad json: %d", resp.StatusCode)
	}
	readBody(t, resp)

	req, _ := http.NewRequest(http.MethodPost, env.client.URL+"/v1/chat/completions", strings.NewReader(`{}`))
	resp, err := http.DefaultClient.Do(req) // 无 token
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("no token: %d", resp.StatusCode)
	}
	readBody(t, resp)
}

// ---------- 流式与 models 测试 ----------

func TestEngineStreamPassthroughAndLog(t *testing.T) {
	up := newFakeUpstream(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"你\"}}]}\n\n")
		fl.Flush()
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"好\"}}]}\n\n")
		fl.Flush()
		io.WriteString(w, "data: [DONE]\n\n")
		fl.Flush()
	})
	defer up.close()

	env := newTestEnv(t, 5*time.Second)
	m, _ := env.st.CreateModel(context.Background(), "m1", nil, nil, nil)
	env.addChannel(t, "ch", up.url(), 1, m.ID, "m1")

	resp := env.chat(t, `{"model":"m1","messages":[],"stream":true}`)
	body := readBody(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `"content":"你"`) || !strings.Contains(body, "[DONE]") {
		t.Errorf("stream body incomplete: %q", body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
	logs, total, _ := env.st.ListLogs(context.Background(), store.LogFilter{Page: 1, Size: 10})
	if total != 1 || !logs[0].Stream || logs[0].Status != "success" {
		t.Fatalf("stream log: %+v", logs)
	}
	if !strings.Contains(logs[0].ResponseBody, "[DONE]") {
		t.Errorf("assembled SSE body missing: %q", logs[0].ResponseBody)
	}
}

func TestEngineStreamFirstByteTimeoutFailover(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second) // 永不及时返回响应头
	}))
	defer slow.Close()
	fast := newFakeUpstream(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"快\"}}]}\n\ndata: [DONE]\n\n")
	})
	defer fast.close()

	env := newTestEnv(t, 150*time.Millisecond) // 首字节超时 150ms
	m, _ := env.st.CreateModel(context.Background(), "m1", nil, nil, nil)
	env.addChannel(t, "slow", slow.URL, 10, m.ID, "m1")
	env.addChannel(t, "fast", fast.url(), 1, m.ID, "m1")

	resp := env.chat(t, `{"model":"m1","messages":[],"stream":true}`)
	body := readBody(t, resp)
	if resp.StatusCode != 200 || !strings.Contains(body, "快") {
		t.Fatalf("should failover to fast channel: %d %q", resp.StatusCode, body)
	}
	_, total, _ := env.st.ListLogs(context.Background(), store.LogFilter{Page: 1, Size: 10})
	if total != 2 {
		t.Fatalf("expected 2 attempts, got %d", total)
	}
}

// finishStream 单元级：上游中途断流
func TestFinishStreamUpstreamInterrupt(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ch, _ := st.CreateChannel(context.Background(), store.Channel{Name: "c", Type: "openai", BaseURL: "http://x", APIKey: "k"})
	reg := provider.NewRegistry()
	e := NewEngine(st, reg, 3)

	src := io.NopCloser(&failAfterReader{data: []byte("data: partial\n\n")})
	entry := &store.LogEntry{RequestID: "r1", Attempt: 1, Status: "success", Stream: true}
	rec := httptest.NewRecorder()
	e.finishStream(rec, src, entry, ch.ID)

	if entry.Status != "failed" || !strings.Contains(entry.Error, "stream interrupted") {
		t.Errorf("entry: %+v", entry)
	}
	if !strings.Contains(rec.Body.String(), "partial") {
		t.Errorf("partial content must reach client: %q", rec.Body.String())
	}
	logs, total, _ := st.ListLogs(context.Background(), store.LogFilter{Page: 1, Size: 10})
	if total != 1 || logs[0].ResponseBody != "data: partial\n\n" {
		t.Errorf("partial body must be logged: %+v", logs)
	}
	got, _ := st.GetChannel(context.Background(), ch.ID)
	if got.ConsecutiveFailures != 1 {
		t.Error("mid-stream failure must count")
	}
}

type failAfterReader struct {
	data []byte
	done bool
}

func (f *failAfterReader) Read(p []byte) (int, error) {
	if f.done {
		return 0, errors.New("connection reset")
	}
	f.done = true
	return copy(p, f.data), nil
}

// finishStream 单元级：客户端断开不计渠道失败
func TestFinishStreamClientDisconnect(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	ch, _ := st.CreateChannel(context.Background(), store.Channel{Name: "c", Type: "openai", BaseURL: "http://x", APIKey: "k"})
	e := NewEngine(st, provider.NewRegistry(), 3)

	src := io.NopCloser(strings.NewReader("data: a\n\ndata: b\n\n"))
	entry := &store.LogEntry{RequestID: "r2", Attempt: 1, Stream: true}
	e.finishStream(&brokenWriter{header: http.Header{}}, src, entry, ch.ID)

	if entry.Status != "failed" || !strings.Contains(entry.Error, "client disconnected") {
		t.Errorf("entry: %+v", entry)
	}
	got, _ := st.GetChannel(context.Background(), ch.ID)
	if got.ConsecutiveFailures != 0 {
		t.Error("client disconnect must NOT count as channel failure")
	}
}

type brokenWriter struct {
	header http.Header
}

func (b *brokenWriter) Header() http.Header       { return b.header }
func (b *brokenWriter) WriteHeader(int)           {}
func (b *brokenWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }
func (b *brokenWriter) Flush()                    {}

func TestModelsEndpoint(t *testing.T) {
	up := newFakeUpstream(func(w http.ResponseWriter, r *http.Request) {})
	defer up.close()

	env := newTestEnv(t, 5*time.Second)
	m, _ := env.st.CreateModel(context.Background(), "glm-5.3", nil, ptr(131072), ptr(8192))
	env.st.CreateModel(context.Background(), "hidden", nil, nil, nil) // 无渠道绑定
	env.addChannel(t, "ch", up.url(), 1, m.ID, "")

	req, _ := http.NewRequest(http.MethodGet, env.client.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+env.tok.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body := readBody(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if !strings.Contains(body, `"id":"glm-5.3"`) || strings.Contains(body, "hidden") {
		t.Errorf("models list wrong: %s", body)
	}
	if !strings.Contains(body, `"context_length":131072`) {
		t.Errorf("context_length missing: %s", body)
	}
}

func ptr(v int64) *int64 { return &v }
