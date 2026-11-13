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

	"github.com/go-chi/chi/v5"

	"pengapi/internal/auth"
	"pengapi/internal/relay/provider"
	"pengapi/internal/store"
)

type adminEnv struct {
	st     *store.Store
	h      *Handler
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
	env := &adminEnv{st: st, h: h, srv: srv}
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

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// 建一个渠道，返回 id
func (e *adminEnv) createChannel(t *testing.T, name, baseURL string) int64 {
	t.Helper()
	resp := e.call(t, http.MethodPost, "/api/channels",
		`{"name":"`+name+`","base_url":"`+baseURL+`","api_key":"k","priority":1}`)
	v := readJSON(t, resp)
	if resp.StatusCode != 201 {
		t.Fatalf("create channel: %d %v", resp.StatusCode, v)
	}
	return int64(v["id"].(float64))
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
	chID := env.createChannel(t, "zhipu", "https://api.bigmodel.cn/coding/paas/v4")

	// 创建模型实体
	resp := env.call(t, http.MethodPost, "/api/models",
		`{"channel_id":`+itoa(chID)+`,"name":"glm-5.3-free"}`)
	v := readJSON(t, resp)
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %v", resp.StatusCode, v)
	}
	id := int64(v["id"].(float64))
	if v["channel_id"].(float64) != float64(chID) || v["name"] != "glm-5.3-free" {
		t.Errorf("created model: %v", v)
	}

	// 同渠道重名 → 幂等返回同一实体（不报错）
	resp = env.call(t, http.MethodPost, "/api/models",
		`{"channel_id":`+itoa(chID)+`,"name":"glm-5.3-free"}`)
	v = readJSON(t, resp)
	if resp.StatusCode != 201 || int64(v["id"].(float64)) != id {
		t.Errorf("duplicate create must be idempotent: %d %v", resp.StatusCode, v)
	}

	// 渠道不存在 → 400
	resp = env.call(t, http.MethodPost, "/api/models", `{"channel_id":9999,"name":"m"}`)
	readJSON(t, resp)
	if resp.StatusCode != 400 {
		t.Errorf("missing channel: %d", resp.StatusCode)
	}

	// 缺字段 → 400
	resp = env.call(t, http.MethodPost, "/api/models", `{"name":"m"}`)
	readJSON(t, resp)
	if resp.StatusCode != 400 {
		t.Errorf("missing channel_id: %d", resp.StatusCode)
	}

	// 列表：带渠道名与绑定标准名
	resp = env.call(t, http.MethodGet, "/api/models", "")
	v = readJSON(t, resp)
	data := v["data"].([]any)
	if len(data) != 1 || data[0].(map[string]any)["name"] != "glm-5.3-free" {
		t.Fatalf("list: %v", v)
	}
	row := data[0].(map[string]any)
	if row["channel_name"] != "zhipu" {
		t.Errorf("channel_name: %v", row)
	}
	if mappings, ok := row["mappings"].([]any); !ok || len(mappings) != 0 {
		t.Errorf("mappings should be empty: %v", row["mappings"])
	}

	// 改名
	resp = env.call(t, http.MethodPut, "/api/models/"+itoa(id), `{"name":"glm-5.3"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("update: %d", resp.StatusCode)
	}
	readJSON(t, resp)
	got, _ := env.st.GetModel(context.Background(), id)
	if got == nil || got.Name != "glm-5.3" {
		t.Errorf("rename failed: %+v", got)
	}

	// 删除
	resp = env.call(t, http.MethodDelete, "/api/models/"+itoa(id), "")
	if resp.StatusCode != 200 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	readJSON(t, resp)
}

// 模型侧反向编辑绑定：PUT /api/models/{id} 带 mapping_ids 时全量替换该模型与标准名的绑定
func TestModelAPIBindMappings(t *testing.T) {
	env := newAdminEnv(t)
	chID := env.createChannel(t, "zhipu", "http://x")

	resp := env.call(t, http.MethodPost, "/api/models",
		`{"channel_id":`+itoa(chID)+`,"name":"glm-free"}`)
	v := readJSON(t, resp)
	if resp.StatusCode != 201 {
		t.Fatalf("create model: %d %v", resp.StatusCode, v)
	}
	modelID := int64(v["id"].(float64))

	resp = env.call(t, http.MethodPost, "/api/mappings", `{"name":"glm-5.3"}`)
	mpA := int64(readJSON(t, resp)["id"].(float64))
	resp = env.call(t, http.MethodPost, "/api/mappings", `{"name":"claude-sonnet"}`)
	mpB := int64(readJSON(t, resp)["id"].(float64))

	// 从 /api/models 读取供编辑弹窗回显的 mapping_ids
	idsOf := func() []int64 {
		r := env.call(t, http.MethodGet, "/api/models", "")
		list := readJSON(t, r)["data"].([]any)
		raw, ok := list[0].(map[string]any)["mapping_ids"].([]any)
		if !ok {
			t.Fatalf("mapping_ids missing: %v", list[0])
		}
		out := make([]int64, 0, len(raw))
		for _, x := range raw {
			out = append(out, int64(x.(float64)))
		}
		return out
	}
	sameSet := func(got []int64, want ...int64) bool {
		if len(got) != len(want) {
			return false
		}
		set := map[int64]bool{}
		for _, id := range got {
			set[id] = true
		}
		for _, id := range want {
			if !set[id] {
				return false
			}
		}
		return true
	}
	// 映射页视角：标准名绑定了哪些模型实体
	boundOf := func(name string) []any {
		r := env.call(t, http.MethodGet, "/api/mappings", "")
		for _, row := range readJSON(t, r)["data"].([]any) {
			m := row.(map[string]any)
			if m["name"] == name {
				return m["bound_models"].([]any)
			}
		}
		t.Fatalf("mapping %q not found", name)
		return nil
	}

	// 编辑模型时绑定两个标准名，映射页应同步看到
	resp = env.call(t, http.MethodPut, "/api/models/"+itoa(modelID),
		`{"name":"glm-free","mapping_ids":[`+itoa(mpA)+`,`+itoa(mpB)+`]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("bind mappings: %d", resp.StatusCode)
	}
	readJSON(t, resp)
	if ids := idsOf(); !sameSet(ids, mpA, mpB) {
		t.Errorf("mapping_ids = %v, want [%d %d]", ids, mpA, mpB)
	}
	if n := len(boundOf("glm-5.3")); n != 1 {
		t.Errorf("glm-5.3 bound models = %d, want 1", n)
	}
	if n := len(boundOf("claude-sonnet")); n != 1 {
		t.Errorf("claude-sonnet bound models = %d, want 1", n)
	}

	// 只改名、不带 mapping_ids → 绑定保持不变
	resp = env.call(t, http.MethodPut, "/api/models/"+itoa(modelID), `{"name":"glm-free-v2"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("rename: %d", resp.StatusCode)
	}
	readJSON(t, resp)
	if ids := idsOf(); !sameSet(ids, mpA, mpB) {
		t.Errorf("rename must keep bindings: %v", ids)
	}

	// 无效 mapping_id → 400，且改名与绑定都不能留下半成品
	resp = env.call(t, http.MethodPut, "/api/models/"+itoa(modelID),
		`{"name":"glm-free-v3","mapping_ids":[9999]}`)
	readJSON(t, resp)
	if resp.StatusCode != 400 {
		t.Errorf("invalid mapping_ids: %d", resp.StatusCode)
	}
	if ids := idsOf(); !sameSet(ids, mpA, mpB) {
		t.Errorf("failed update must not touch bindings: %v", ids)
	}
	got, _ := env.st.GetModel(context.Background(), modelID)
	if got == nil || got.Name != "glm-free-v2" {
		t.Errorf("failed update must roll back rename: %+v", got)
	}

	// 空数组 → 清空绑定
	resp = env.call(t, http.MethodPut, "/api/models/"+itoa(modelID),
		`{"name":"glm-free-v2","mapping_ids":[]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("clear: %d", resp.StatusCode)
	}
	readJSON(t, resp)
	if ids := idsOf(); len(ids) != 0 {
		t.Errorf("empty mapping_ids must clear bindings: %v", ids)
	}
	if n := len(boundOf("glm-5.3")); n != 0 {
		t.Errorf("glm-5.3 bound models = %d, want 0", n)
	}

	// 模型不存在 → 404
	resp = env.call(t, http.MethodPut, "/api/models/9999", `{"name":"x","mapping_ids":[]}`)
	readJSON(t, resp)
	if resp.StatusCode != 404 {
		t.Errorf("missing model: %d", resp.StatusCode)
	}
}

func TestMappingsAPI(t *testing.T) {
	env := newAdminEnv(t)
	chID := env.createChannel(t, "zhipu", "http://x")

	// 造两个模型实体
	resp := env.call(t, http.MethodPost, "/api/models", `{"channel_id":`+itoa(chID)+`,"name":"glm-free"}`)
	m1 := int64(readJSON(t, resp)["id"].(float64))
	resp = env.call(t, http.MethodPost, "/api/models", `{"channel_id":`+itoa(chID)+`,"name":"glm-pro"}`)
	m2 := int64(readJSON(t, resp)["id"].(float64))

	// 创建映射（带参数 + 绑定）
	resp = env.call(t, http.MethodPost, "/api/mappings",
		`{"name":"glm-5.3","context_length":131072,"max_output_tokens":8192,"model_ids":[`+itoa(m1)+`,`+itoa(m2)+`]}`)
	v := readJSON(t, resp)
	if resp.StatusCode != 201 {
		t.Fatalf("create mapping: %d %v", resp.StatusCode, v)
	}
	mpID := int64(v["id"].(float64))
	if len(v["bound_models"].([]any)) != 2 {
		t.Errorf("bound_models: %v", v)
	}

	// 重名 → 400
	resp = env.call(t, http.MethodPost, "/api/mappings", `{"name":"glm-5.3"}`)
	readJSON(t, resp)
	if resp.StatusCode != 400 {
		t.Errorf("duplicate name: %d", resp.StatusCode)
	}
	// 绑定不存在的模型 → 400
	resp = env.call(t, http.MethodPost, "/api/mappings", `{"name":"bad","model_ids":[9999]}`)
	readJSON(t, resp)
	if resp.StatusCode != 400 {
		t.Errorf("invalid model_ids: %d", resp.StatusCode)
	}
	// 缺 name → 400
	resp = env.call(t, http.MethodPost, "/api/mappings", `{}`)
	readJSON(t, resp)
	if resp.StatusCode != 400 {
		t.Errorf("missing name: %d", resp.StatusCode)
	}

	// 列表
	resp = env.call(t, http.MethodGet, "/api/mappings", "")
	v = readJSON(t, resp)
	list := v["data"].([]any)
	if len(list) != 1 {
		t.Fatalf("list: %v", v)
	}
	row := list[0].(map[string]any)
	if row["name"] != "glm-5.3" || row["context_length"].(float64) != 131072 {
		t.Errorf("mapping row: %v", row)
	}
	bound := row["bound_models"].([]any)
	if len(bound) != 2 || bound[0].(map[string]any)["channel_name"] != "zhipu" {
		t.Errorf("bound models: %v", bound)
	}

	// 全量更新：改参数 + 绑定整体替换为只剩 m2
	resp = env.call(t, http.MethodPut, "/api/mappings/"+itoa(mpID),
		`{"name":"glm-5.3","context_length":200000,"model_ids":[`+itoa(m2)+`]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("update: %d", resp.StatusCode)
	}
	readJSON(t, resp)
	mps, _ := env.st.ListMappings(context.Background())
	if len(mps) != 1 || len(mps[0].BoundModels) != 1 || mps[0].BoundModels[0].ID != m2 ||
		mps[0].ContextLength == nil || *mps[0].ContextLength != 200000 || mps[0].MaxOutputTokens != nil {
		t.Errorf("update not applied: %+v", mps[0])
	}

	// 更新不存在的映射 → 404
	resp = env.call(t, http.MethodPut, "/api/mappings/9999", `{"name":"x"}`)
	readJSON(t, resp)
	if resp.StatusCode != 404 {
		t.Errorf("missing mapping: %d", resp.StatusCode)
	}

	// 删除
	resp = env.call(t, http.MethodDelete, "/api/mappings/"+itoa(mpID), "")
	if resp.StatusCode != 200 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	readJSON(t, resp)
	mps, _ = env.st.ListMappings(context.Background())
	if len(mps) != 0 {
		t.Errorf("mapping should be deleted: %+v", mps)
	}
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

	// 详情接口返回完整 token（管理端复制用）
	resp = env.call(t, http.MethodGet, "/api/tokens/"+itoa(id), "")
	v = readJSON(t, resp)
	if resp.StatusCode != 200 || v["token"].(string) != full {
		t.Errorf("reveal: %d %v", resp.StatusCode, v)
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

func TestChannelsAPI(t *testing.T) {
	env := newAdminEnv(t)

	// 创建渠道（不含模型绑定）
	resp := env.call(t, http.MethodPost, "/api/channels", `{
		"name":"zhipu","base_url":"https://api.bigmodel.cn/coding/paas/v4","api_key":"k1",
		"priority":10,"test_model":"glm-5.3-free"}`)
	v := readJSON(t, resp)
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %v", resp.StatusCode, v)
	}
	chID := int64(v["id"].(float64))

	// 列表：无绑定/健康状态字段
	resp = env.call(t, http.MethodGet, "/api/channels", "")
	v = readJSON(t, resp)
	ch := v["data"].([]any)[0].(map[string]any)
	if ch["name"] != "zhipu" || ch["priority"].(float64) != 10 || ch["enabled"] != true {
		t.Fatalf("list: %v", ch)
	}
	for _, gone := range []string{"models", "auto_disabled", "consecutive_failures", "disabled_until"} {
		if _, ok := ch[gone]; ok {
			t.Errorf("field %s must be gone: %v", gone, ch)
		}
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
		"enabled":true,"test_model":"m"}`)
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

	// reset 端点已删除 → 404
	resp = env.call(t, http.MethodPost, "/api/channels/"+itoa(chID)+"/reset", "")
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("reset endpoint must be gone: %d", resp.StatusCode)
	}

	// 删除
	resp = env.call(t, http.MethodDelete, "/api/channels/"+itoa(chID), "")
	if resp.StatusCode != 200 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	readJSON(t, resp)
}

func TestFetchModelsAndAddChannelModels(t *testing.T) {
	// 假上游 /models：一个已存在为模型实体，一个不存在
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
	chID := env.createChannel(t, "up", upstream.URL)

	// 先有一个模型实体
	resp := env.call(t, http.MethodPost, "/api/channels/"+itoa(chID)+"/models", `{"names":["GLM5.3"]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("add models: %d", resp.StatusCode)
	}
	readJSON(t, resp)

	// fetch-models：GLM5.3 已存在，new-model-x 不存在
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
	if first["name"] != "GLM5.3" || first["exists"] != true {
		t.Errorf("exists flag wrong: %v", first)
	}
	second := data[1].(map[string]any)
	if second["name"] != "new-model-x" || second["exists"] != false {
		t.Errorf("exists flag wrong: %v", second)
	}
	if second["context_length"].(float64) != 64000 {
		t.Errorf("upstream params should pass through: %v", second)
	}

	// 批量添加：已存在的跳过、新的创建（幂等，重复提交不报错）
	resp = env.call(t, http.MethodPost, "/api/channels/"+itoa(chID)+"/models",
		`{"names":["GLM5.3","new-model-x"]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("add models 2: %d", resp.StatusCode)
	}
	readJSON(t, resp)
	models, _ := env.st.ModelsOfChannel(context.Background(), chID)
	if len(models) != 2 {
		t.Fatalf("expected 2 model entities: %+v", models)
	}

	// 空 names → 400
	resp = env.call(t, http.MethodPost, "/api/channels/"+itoa(chID)+"/models", `{"names":[]}`)
	readJSON(t, resp)
	if resp.StatusCode != 400 {
		t.Errorf("empty names: %d", resp.StatusCode)
	}

	// 渠道不存在 → 404
	resp = env.call(t, http.MethodPost, "/api/channels/999/fetch-models", "")
	readJSON(t, resp)
	if resp.StatusCode != 404 {
		t.Errorf("missing channel: %d", resp.StatusCode)
	}
	resp = env.call(t, http.MethodPost, "/api/channels/999/models", `{"names":["x"]}`)
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
		t.Errorf("response body: %v", v)
	}
	if !strings.Contains(gotBody, "cheap-model") {
		t.Errorf("upstream got: %s", gotBody)
	}

	// 不写日志
	_, total, _ := env.st.ListLogs(context.Background(), store.LogFilter{Page: 1, Size: 10})
	if total != 0 {
		t.Error("ping must not write request_logs")
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
		RequestID: "req-a", Attempt: 1, ModelRequested: "m1", ModelCanonical: "m1", ModelActual: "m1-free",
		ChannelName: "c1", Status: "failed", Error: "boom", RequestBody: "{}", ResponseBody: "err",
	})
	secondID, _ := env.st.InsertLog(ctx, store.LogEntry{
		RequestID: "req-a", Attempt: 2, ModelRequested: "m1", ModelCanonical: "m1", ModelActual: "m1-paid",
		ChannelName: "c2", Status: "success", RequestBody: "{}", ResponseBody: "ok",
	})

	// 列表 + 分页字段
	resp := env.call(t, http.MethodGet, "/api/logs?page=1&size=10", "")
	v := readJSON(t, resp)
	if resp.StatusCode != 200 || v["total"].(float64) != 2 {
		t.Fatalf("list: %d %v", resp.StatusCode, v)
	}
	// 列表接口要把实际模型名返回给前端
	list := v["data"].([]any)
	if len(list) != 2 || list[0].(map[string]any)["model_actual"] != "m1-paid" {
		t.Errorf("model_actual in list: %v", list)
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
	if v["data"].(map[string]any)["model_actual"] != "m1-paid" ||
		attempts[0].(map[string]any)["model_actual"] != "m1-free" {
		t.Errorf("model_actual in detail: %v", v)
	}

	// 不存在 → 404
	resp = env.call(t, http.MethodGet, "/api/logs/999", "")
	readJSON(t, resp)
	if resp.StatusCode != 404 {
		t.Errorf("missing log: %d", resp.StatusCode)
	}
}

func TestMappingsAPIParameterOverrides(t *testing.T) {
	env := newAdminEnv(t)
	chID := env.createChannel(t, "zhipu", "http://x")
	resp := env.call(t, http.MethodPost, "/api/models", `{"channel_id":`+itoa(chID)+`,"name":"glm-free"}`)
	mID := int64(readJSON(t, resp)["id"].(float64))

	// 创建带强制覆盖
	resp = env.call(t, http.MethodPost, "/api/mappings",
		`{"name":"forced","model_ids":[`+itoa(mID)+`],"parameter_overrides":{"temperature":1,"stream":false}}`)
	v := readJSON(t, resp)
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %v", resp.StatusCode, v)
	}
	ov := v["parameter_overrides"].(map[string]any)
	if ov["temperature"].(float64) != 1 || ov["stream"] != false {
		t.Errorf("create overrides: %v", ov)
	}
	mpID := int64(v["id"].(float64))

	// 空参数名 → 400
	resp = env.call(t, http.MethodPost, "/api/mappings",
		`{"name":"bad-ov","parameter_overrides":{"  ":1}}`)
	readJSON(t, resp)
	if resp.StatusCode != 400 {
		t.Errorf("blank override key: %d", resp.StatusCode)
	}

	// 列表带回覆盖
	resp = env.call(t, http.MethodGet, "/api/mappings", "")
	list := readJSON(t, resp)["data"].([]any)
	row := list[0].(map[string]any)
	if row["parameter_overrides"].(map[string]any)["temperature"].(float64) != 1 {
		t.Errorf("list overrides: %v", row)
	}

	// 更新整体替换；不传字段则清空
	resp = env.call(t, http.MethodPut, "/api/mappings/"+itoa(mpID),
		`{"name":"forced","model_ids":[`+itoa(mID)+`],"parameter_overrides":{"max_tokens":1024}}`)
	if resp.StatusCode != 200 {
		t.Fatalf("update: %d", resp.StatusCode)
	}
	readJSON(t, resp)
	mps, _ := env.st.ListMappings(context.Background())
	if len(mps[0].ParameterOverrides) != 1 || mps[0].ParameterOverrides["max_tokens"] != float64(1024) {
		t.Errorf("update overrides: %+v", mps[0].ParameterOverrides)
	}
	resp = env.call(t, http.MethodPut, "/api/mappings/"+itoa(mpID),
		`{"name":"forced","model_ids":[`+itoa(mID)+`]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("update clear: %d", resp.StatusCode)
	}
	readJSON(t, resp)
	mps, _ = env.st.ListMappings(context.Background())
	if mps[0].ParameterOverrides != nil {
		t.Errorf("overrides should be cleared: %+v", mps[0].ParameterOverrides)
	}
}
