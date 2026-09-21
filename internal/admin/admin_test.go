package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"pengapi/internal/auth"
	"pengapi/internal/relay/provider"
	"pengapi/internal/store"

	"github.com/go-chi/chi/v5"
)

type adminEnv struct {
	st     *store.Store
	srv    *httptest.Server
	cookie *http.Cookie // 已登录的 session cookie
}

func newAdminEnv(t *testing.T) *adminEnv {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ss := auth.NewSessionStore(time.Hour)
	h := New(st, ss, "testpass", provider.NewRegistry(
		provider.NewOpenAI(&http.Client{}, 10*time.Second, 5*time.Second)))
	r := chi.NewRouter()
	h.RegisterRoutes(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	env := &adminEnv{st: st, srv: srv}
	env.cookie = env.login(t, "testpass")
	return env
}

func (e *adminEnv) login(t *testing.T, password string) *http.Cookie {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/api/login", strings.NewReader(`{"password":"`+password+`"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login status = %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookie {
			return c
		}
	}
	t.Fatal("no session cookie set")
	return nil
}

// 带 session 的管理 API 请求
func (e *adminEnv) call(t *testing.T, method, path string, body string) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if e.cookie != nil {
		req.AddCookie(e.cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func readJSON(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var v map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return v
}

func TestLoginFlow(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	ss := auth.NewSessionStore(time.Hour)
	h := New(st, ss, "testpass", provider.NewRegistry())
	r := chi.NewRouter()
	h.RegisterRoutes(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	// 错误密码 → 401
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/login", strings.NewReader(`{"password":"wrong"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 401 {
		t.Errorf("wrong password: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 未登录访问受保护接口 → 401
	resp, _ = http.Get(srv.URL + "/api/me")
	if resp.StatusCode != 401 {
		t.Errorf("unauth me: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 正确密码 → 200 + cookie；me → 200；logout 后 me → 401
	env := &adminEnv{st: st, srv: srv}
	env.cookie = env.login(t, "testpass")
	resp = env.call(t, http.MethodGet, "/api/me", "")
	if resp.StatusCode != 200 {
		t.Errorf("me: %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = env.call(t, http.MethodPost, "/api/logout", "")
	resp.Body.Close()
	resp = env.call(t, http.MethodGet, "/api/me", "")
	if resp.StatusCode != 401 {
		t.Errorf("after logout: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestModelsAPI(t *testing.T) {
	env := newAdminEnv(t)

	// 创建
	resp := env.call(t, http.MethodPost, "/api/models",
		`{"name":"glm-5.3","aliases":["GLM5.3"],"context_length":131072}`)
	v := readJSON(t, resp)
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %v", resp.StatusCode, v)
	}
	id := int64(v["id"].(float64))

	// 重名 → 400
	resp = env.call(t, http.MethodPost, "/api/models", `{"name":"glm-5.3","aliases":[]}`)
	readJSON(t, resp)
	if resp.StatusCode != 400 {
		t.Errorf("duplicate name: %d", resp.StatusCode)
	}

	// 列表
	resp = env.call(t, http.MethodGet, "/api/models", "")
	v = readJSON(t, resp)
	data := v["data"].([]any)
	if len(data) != 1 || data[0].(map[string]any)["name"] != "glm-5.3" {
		t.Fatalf("list: %v", v)
	}
	aliases := data[0].(map[string]any)["aliases"].([]any)
	if len(aliases) != 1 || aliases[0] != "GLM5.3" {
		t.Errorf("aliases: %v", aliases)
	}

	// 更新（别名整体替换）
	resp = env.call(t, http.MethodPut, "/api/models/"+itoa(id),
		`{"name":"glm-5.3","aliases":["glm53"],"max_output_tokens":8192}`)
	if resp.StatusCode != 200 {
		t.Fatalf("update: %d", resp.StatusCode)
	}
	readJSON(t, resp)

	// 删除
	resp = env.call(t, http.MethodDelete, "/api/models/"+itoa(id), "")
	if resp.StatusCode != 200 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	readJSON(t, resp)
}

func TestTokensAPI(t *testing.T) {
	env := newAdminEnv(t)

	// 创建 → 完整 token 仅此一次
	resp := env.call(t, http.MethodPost, "/api/tokens", `{"name":"cline"}`)
	v := readJSON(t, resp)
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %v", resp.StatusCode, v)
	}
	full := v["token"].(string)
	if !strings.HasPrefix(full, "sk-") {
		t.Fatalf("token: %v", v)
	}
	id := int64(v["id"].(float64))

	// 列表里必须打码
	resp = env.call(t, http.MethodGet, "/api/tokens", "")
	v = readJSON(t, resp)
	masked := v["data"].([]any)[0].(map[string]any)["token"].(string)
	if masked == full || !strings.HasPrefix(masked, "sk-****") {
		t.Errorf("token must be masked in list: %q", masked)
	}

	// 更新
	resp = env.call(t, http.MethodPut, "/api/tokens/"+itoa(id), `{"name":"cline2","enabled":false}`)
	if resp.StatusCode != 200 {
		t.Fatalf("update: %d", resp.StatusCode)
	}
	readJSON(t, resp)

	// 删除
	resp = env.call(t, http.MethodDelete, "/api/tokens/"+itoa(id), "")
	if resp.StatusCode != 200 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	readJSON(t, resp)
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func TestChannelsAPI(t *testing.T) {
	env := newAdminEnv(t)

	// 先建模型供绑定
	resp := env.call(t, http.MethodPost, "/api/models", `{"name":"glm-5.3","aliases":[]}`)
	mv := readJSON(t, resp)
	modelID := int64(mv["id"].(float64))

	// 创建渠道（含绑定）
	resp = env.call(t, http.MethodPost, "/api/channels", `{
		"name":"zhipu","base_url":"https://api.bigmodel.cn/coding/paas/v4","api_key":"k1",
		"priority":10,"test_model":"glm-5.3-free",
		"models":[{"model_id":`+itoa(modelID)+`,"upstream_model":"glm-5.3-free"}]}`)
	v := readJSON(t, resp)
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %v", resp.StatusCode, v)
	}
	chID := int64(v["id"].(float64))

	// 列表：含绑定与状态字段
	resp = env.call(t, http.MethodGet, "/api/channels", "")
	v = readJSON(t, resp)
	ch := v["data"].([]any)[0].(map[string]any)
	if ch["name"] != "zhipu" || ch["priority"].(float64) != 10 || ch["enabled"] != true {
		t.Fatalf("list: %v", ch)
	}
	bindings := ch["models"].([]any)
	if len(bindings) != 1 || bindings[0].(map[string]any)["upstream_model"] != "glm-5.3-free" {
		t.Errorf("bindings: %v", bindings)
	}

	// 缺必填字段 → 400
	resp = env.call(t, http.MethodPost, "/api/channels", `{"name":"x"}`)
	readJSON(t, resp)
	if resp.StatusCode != 400 {
		t.Errorf("missing fields: %d", resp.StatusCode)
	}

	// 更新
	resp = env.call(t, http.MethodPut, "/api/channels/"+itoa(chID), `{
		"name":"zhipu2","base_url":"https://example.com/v1","api_key":"k2","priority":5,
		"enabled":true,"test_model":"m","models":[{"model_id":`+itoa(modelID)+`,"upstream_model":""}]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("update: %d", resp.StatusCode)
	}
	readJSON(t, resp)

	// toggle 禁用
	resp = env.call(t, http.MethodPost, "/api/channels/"+itoa(chID)+"/toggle", `{"enabled":false}`)
	readJSON(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("toggle: %d", resp.StatusCode)
	}
	got, _ := env.st.GetChannel(context.Background(), chID)
	if got.Enabled {
		t.Error("channel should be disabled")
	}

	// reset 清失败状态
	env.st.RecordFailure(context.Background(), chID, 1) // threshold=1 → 必禁用
	resp = env.call(t, http.MethodPost, "/api/channels/"+itoa(chID)+"/reset", "")
	readJSON(t, resp)
	got, _ = env.st.GetChannel(context.Background(), chID)
	if got.AutoDisabled || got.ConsecutiveFailures != 0 {
		t.Errorf("reset failed: %+v", got)
	}

	// 删除
	resp = env.call(t, http.MethodDelete, "/api/channels/"+itoa(chID), "")
	if resp.StatusCode != 200 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	readJSON(t, resp)
}

func TestFetchModelsAndBind(t *testing.T) {
	// 假上游 /models：一个能匹配已有模型，一个不能
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			io.WriteString(w, `{"object":"list","data":[
				{"id":"GLM5.3","object":"model"},
				{"id":"new-model-x","object":"model","context_length":64000}
			]}`)
			return
		}
		w.WriteHeader(404)
	}))
	defer upstream.Close()

	env := newAdminEnv(t)
	resp := env.call(t, http.MethodPost, "/api/models", `{"name":"glm-5.3","aliases":["GLM5.3"]}`)
	mv := readJSON(t, resp)
	modelID := int64(mv["id"].(float64))

	resp = env.call(t, http.MethodPost, "/api/channels",
		`{"name":"up","base_url":"`+upstream.URL+`","api_key":"k","priority":1}`)
	cv := readJSON(t, resp)
	chID := int64(cv["id"].(float64))

	// fetch-models：GLM5.3 应预匹配到已有模型，new-model-x 建议新建
	resp = env.call(t, http.MethodPost, "/api/channels/"+itoa(chID)+"/fetch-models", "")
	v := readJSON(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("fetch-models: %d %v", resp.StatusCode, v)
	}
	data := v["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("expected 2 upstream models: %v", data)
	}
	first := data[0].(map[string]any)
	if first["upstream_id"] != "GLM5.3" || first["suggested_model_id"].(float64) != float64(modelID) {
		t.Errorf("pre-match failed: %v", first)
	}
	second := data[1].(map[string]any)
	if second["suggested_model_id"] != nil || second["suggested_name"] != "new-model-x" {
		t.Errorf("new model suggestion wrong: %v", second)
	}

	// 批量绑定：一个绑已有，一个新建
	resp = env.call(t, http.MethodPost, "/api/channels/"+itoa(chID)+"/models", `{"bindings":[
		{"upstream_model":"GLM5.3","model_id":`+itoa(modelID)+`},
		{"upstream_model":"new-model-x","new_model_name":"new-model-x","context_length":64000}
	]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("bind: %d", resp.StatusCode)
	}
	readJSON(t, resp)

	got, _ := env.st.GetChannel(context.Background(), chID)
	if len(got.Models) != 2 {
		t.Fatalf("bindings: %+v", got.Models)
	}
	// 新建的模型带参数、且自身为别名
	m, err := env.st.ResolveModel(context.Background(), "NEW-MODEL-X")
	if err != nil || m == nil || m.ContextLength == nil || *m.ContextLength != 64000 {
		t.Errorf("auto-created model: %v %+v", err, m)
	}

	// 渠道不存在 → 404
	resp = env.call(t, http.MethodPost, "/api/channels/999/fetch-models", "")
	readJSON(t, resp)
	if resp.StatusCode != 404 {
		t.Errorf("missing channel: %d", resp.StatusCode)
	}
}

func TestChannelConnectivityTest(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		io.WriteString(w, `{"choices":[{"message":{"content":"pong"}}]}`)
	}))
	defer upstream.Close()

	env := newAdminEnv(t)
	resp := env.call(t, http.MethodPost, "/api/channels",
		`{"name":"up","base_url":"`+upstream.URL+`","api_key":"k","priority":1,"test_model":"cheap-model"}`)
	cv := readJSON(t, resp)
	chID := int64(cv["id"].(float64))

	resp = env.call(t, http.MethodPost, "/api/channels/"+itoa(chID)+"/test", "")
	v := readJSON(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("test: %d %v", resp.StatusCode, v)
	}
	if v["success"] != true || v["http_status"].(float64) != 200 {
		t.Errorf("test result: %v", v)
	}
	if !strings.Contains(v["request_body"].(string), "cheap-model") ||
		!strings.Contains(v["request_body"].(string), `"max_tokens":1`) {
		t.Errorf("ping request body: %v", v["request_body"])
	}
	if !strings.Contains(v["response_body"].(string), "pong") {
		t.Errorf("response body: %v", v["response_body"])
	}
	if !strings.Contains(gotBody, "cheap-model") {
		t.Errorf("upstream got: %s", gotBody)
	}

	// 不写日志、不计数
	_, total, _ := env.st.ListLogs(context.Background(), store.LogFilter{Page: 1, Size: 10})
	if total != 0 {
		t.Error("ping must not write request_logs")
	}
	got, _ := env.st.GetChannel(context.Background(), chID)
	if got.ConsecutiveFailures != 0 {
		t.Error("ping must not touch failure counters")
	}

	// 未配置 test_model → 400
	resp = env.call(t, http.MethodPost, "/api/channels",
		`{"name":"up2","base_url":"`+upstream.URL+`","api_key":"k"}`)
	cv2 := readJSON(t, resp)
	resp = env.call(t, http.MethodPost, "/api/channels/"+itoa(int64(cv2["id"].(float64)))+"/test", "")
	readJSON(t, resp)
	if resp.StatusCode != 400 {
		t.Errorf("missing test_model: %d", resp.StatusCode)
	}
}

func TestLogsAPI(t *testing.T) {
	env := newAdminEnv(t)
	ctx := context.Background()

	// 造两条日志：同一 request_id 的两次尝试
	env.st.InsertLog(ctx, store.LogEntry{
		RequestID: "req-a", Attempt: 1, ModelRequested: "m1", ModelCanonical: "m1",
		ChannelName: "c1", Status: "failed", Error: "boom", RequestBody: "{}", ResponseBody: "err",
	})
	secondID, _ := env.st.InsertLog(ctx, store.LogEntry{
		RequestID: "req-a", Attempt: 2, ModelRequested: "m1", ModelCanonical: "m1",
		ChannelName: "c2", Status: "success", RequestBody: "{}", ResponseBody: "ok",
	})

	// 列表 + 分页字段
	resp := env.call(t, http.MethodGet, "/api/logs?page=1&size=10", "")
	v := readJSON(t, resp)
	if resp.StatusCode != 200 || v["total"].(float64) != 2 {
		t.Fatalf("list: %d %v", resp.StatusCode, v)
	}

	// 状态筛选
	resp = env.call(t, http.MethodGet, "/api/logs?status=success", "")
	v = readJSON(t, resp)
	if v["total"].(float64) != 1 {
		t.Errorf("status filter: %v", v["total"])
	}

	// 详情 + 降级链路
	resp = env.call(t, http.MethodGet, "/api/logs/"+itoa(secondID), "")
	v = readJSON(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("detail: %d", resp.StatusCode)
	}
	attempts := v["attempts"].([]any)
	if len(attempts) != 2 || attempts[0].(map[string]any)["attempt"].(float64) != 1 {
		t.Errorf("attempts group: %v", attempts)
	}

	// 不存在 → 404
	resp = env.call(t, http.MethodGet, "/api/logs/999", "")
	readJSON(t, resp)
	if resp.StatusCode != 404 {
		t.Errorf("missing log: %d", resp.StatusCode)
	}
}
