package admin

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQuotaServiceOpenRouter(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/auth/key" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer openrouter-key" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"free_model_daily_requests":{"used":58,"limit":100,"remaining":42}}}`))
	}))
	defer upstream.Close()

	svc := newQuotaService()
	svc.client = upstream.Client()
	svc.setURL("openrouter", upstream.URL+"/api/v1/auth/key")
	quota, err := svc.fetch(context.Background(), "openrouter", quotaRequest{APIKey: "openrouter-key"})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if quota.Used == nil || *quota.Used != 58 || quota.Limit == nil || *quota.Limit != 100 || quota.Remaining == nil || *quota.Remaining != 42 {
		t.Fatalf("quota = %+v", quota)
	}
}

func TestQuotaServiceDeepSeek(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/user/balance" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer deepseek-key" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"is_available":false,"balance_infos":[{"currency":"CNY","total_balance":"-0.71","granted_balance":"0.00","topped_up_balance":"-0.71"}]}`))
	}))
	defer upstream.Close()

	svc := newQuotaService()
	svc.client = upstream.Client()
	svc.setURL("deepseek", upstream.URL+"/user/balance")
	quota, err := svc.fetch(context.Background(), "deepseek", quotaRequest{APIKey: "deepseek-key"})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if quota.Balance == nil || *quota.Balance != "-0.71" {
		t.Fatalf("quota = %+v", quota)
	}
}

func TestQuotaServiceKimiRatios(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/coding/v1/usages" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer kimi-key" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"usages":{
			"limit_5h":{"used_ratio":0.85},
			"limit_7d":{"used_ratio":0.8},
			"limit_month_total":{"used_ratio":0.5}
		}}`))
	}))
	defer upstream.Close()

	svc := newQuotaService()
	svc.client = upstream.Client()
	svc.setURL("kimi", upstream.URL+"/coding/v1/usages")
	quota, err := svc.fetch(context.Background(), "kimi", quotaRequest{APIKey: "kimi-key"})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	assertPercent(t, "five_hour", quota.FiveHour, 15)
	assertPercent(t, "weekly", quota.Weekly, 20)
	assertPercent(t, "total", quota.Total, 50)
}

func TestQuotaServiceKimiLegacyAndMissingWindows(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"usage":{"limit":"1000","used":"800","remaining":"200"},
			"limits":[{"window":{"duration":300,"timeUnit":"TIME_UNIT_MINUTE"},"detail":{"limit":"200","used":"50","remaining":"150"}}]
		}`))
	}))
	defer upstream.Close()

	svc := newQuotaService()
	svc.client = upstream.Client()
	svc.setURL("kimi", upstream.URL)
	quota, err := svc.fetch(context.Background(), "kimi", quotaRequest{APIKey: "kimi-key"})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	assertPercent(t, "five_hour", quota.FiveHour, 75)
	assertPercent(t, "weekly", quota.Weekly, 20)
	if quota.Total != nil {
		t.Errorf("total should be absent: %v", *quota.Total)
	}
}

func TestQuotaServiceErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "http status", status: http.StatusUnauthorized, body: `{"error":"bad key"}`},
		{name: "invalid json", status: http.StatusOK, body: `{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer upstream.Close()
			svc := newQuotaService()
			svc.client = upstream.Client()
			svc.setURL("openrouter", upstream.URL)
			if _, err := svc.fetch(context.Background(), "openrouter", quotaRequest{APIKey: "bad"}); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestQuotaTypesDerivedFromRegistry(t *testing.T) {
	svc := newQuotaService()
	types := svc.types()
	if len(types) != len(quotaSources) {
		t.Fatalf("types = %+v", types)
	}
	for _, opt := range types {
		if !validQuotaType(opt.Value) || opt.Label == "" {
			t.Errorf("inconsistent registry entry: %+v", opt)
		}
	}
	if validQuotaType("none") {
		t.Error("unknown quota type must be rejected")
	}
}

func TestChannelQuotaAPI(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"free_model_daily_requests":{"used":58,"limit":100,"remaining":42}}}`))
	}))
	defer upstream.Close()

	env := newAdminEnv(t)
	env.h.quotas.svc.setURL("openrouter", upstream.URL)
	resp := env.call(t, http.MethodPost, "/api/channels", `{"name":"or","base_url":"http://upstream","api_key":"k","quota_type":"openrouter"}`)
	created := readJSON(t, resp)
	if resp.StatusCode != http.StatusCreated || created["quota_type"] != "openrouter" {
		t.Fatalf("create: %d %v", resp.StatusCode, created)
	}
	id := int64(created["id"].(float64))

	resp = env.call(t, http.MethodGet, "/api/channels/"+itoa(id)+"/quota", "")
	got := readJSON(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("quota: %d %v", resp.StatusCode, got)
	}
	data := got["data"].(map[string]any)
	if data["used"].(float64) != 58 || data["limit"].(float64) != 100 || data["remaining"].(float64) != 42 {
		t.Errorf("quota data: %v", data)
	}

	resp = env.call(t, http.MethodGet, "/api/quota-types", "")
	typesResp := readJSON(t, resp)
	if resp.StatusCode != http.StatusOK || len(typesResp["data"].([]any)) == 0 {
		t.Errorf("quota types: %d %v", resp.StatusCode, typesResp)
	}

	resp = env.call(t, http.MethodPut, "/api/channels/"+itoa(id), `{"name":"or","base_url":"http://upstream","api_key":"k","quota_type":"none"}`)
	readJSON(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid quota_type: %d", resp.StatusCode)
	}

	resp = env.call(t, http.MethodPut, "/api/channels/"+itoa(id), `{"name":"or","base_url":"http://upstream","api_key":"k"}`)
	readJSON(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear quota_type: %d", resp.StatusCode)
	}
	resp = env.call(t, http.MethodGet, "/api/channels/"+itoa(id)+"/quota", "")
	readJSON(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unconfigured quota: %d", resp.StatusCode)
	}

	resp = env.call(t, http.MethodGet, "/api/channels/9999/quota", "")
	readJSON(t, resp)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("missing channel: %d", resp.StatusCode)
	}
}

func assertPercent(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil || math.Abs(*got-want) > 1e-9 {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

// new-api 的额度接口地址由渠道 base_url 推导，鉴权用 quota_token 而非 API Key。
func TestQuotaServiceNewAPI(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/subscription/self" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer nap-token" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"subscriptions":[
			{"subscription":{"amount_total":20000000,"amount_used":5000000,"status":"active"}},
			{"subscription":{"amount_total":10000000,"amount_used":0,"status":"active"}},
			{"subscription":{"amount_total":0,"amount_used":0,"status":"cancelled"}}
		]},"success":true}`))
	}))
	defer upstream.Close()

	svc := newQuotaService()
	svc.client = upstream.Client()
	// 不登记覆盖地址：走 base_url 推导（渠道 base_url 带 /v1 前缀也要回到 host）
	quota, err := svc.fetch(context.Background(), "new-api", quotaRequest{BaseURL: upstream.URL + "/v1", Token: "nap-token"})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if quota.Used == nil || *quota.Used != 5000000 || quota.Limit == nil || *quota.Limit != 30000000 ||
		quota.Remaining == nil || *quota.Remaining != 25000000 {
		t.Fatalf("quota = %+v", quota)
	}
	if quota.QuotaPerUnit == nil || *quota.QuotaPerUnit != 500000 {
		t.Fatalf("quota_per_unit = %v", quota.QuotaPerUnit)
	}
}

func TestQuotaServiceNewAPIConfigErrors(t *testing.T) {
	svc := newQuotaService()
	if _, err := svc.fetch(context.Background(), "new-api", quotaRequest{BaseURL: "https://x.test/v1"}); err == nil {
		t.Error("missing token must fail")
	}
	if _, err := svc.fetch(context.Background(), "new-api", quotaRequest{BaseURL: "/v1", Token: "nap"}); err == nil {
		t.Error("base_url without host must fail")
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"subscriptions":[{"subscription":{"amount_total":0,"amount_used":0,"status":"cancelled"}}]},"success":true}`))
	}))
	defer upstream.Close()
	svc.client = upstream.Client()
	if _, err := svc.fetch(context.Background(), "new-api", quotaRequest{BaseURL: upstream.URL, Token: "nap"}); err == nil {
		t.Error("no active subscription must fail")
	}
}

func TestValidateQuotaConfig(t *testing.T) {
	cases := []struct {
		quotaType string
		token     string
		wantErr   bool
	}{
		{quotaType: "", token: "", wantErr: false},
		{quotaType: "openrouter", token: "", wantErr: false},
		{quotaType: "new-api", token: "", wantErr: true},
		{quotaType: "new-api", token: "  ", wantErr: true},
		{quotaType: "new-api", token: "nap_x", wantErr: false},
		{quotaType: "none", token: "", wantErr: true},
	}
	for _, tc := range cases {
		if got := validateQuotaConfig(tc.quotaType, tc.token); (got != "") != tc.wantErr {
			t.Errorf("validateQuotaConfig(%q,%q) = %q", tc.quotaType, tc.token, got)
		}
	}
}

// 渠道侧走完整链路：保存时挡住缺令牌，查询时用 quota_token 请求上游。
func TestChannelQuotaAPINewAPI(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"subscriptions":[{"subscription":{"amount_total":10000000,"amount_used":4000000,"status":"active"}}]},"success":true}`))
	}))
	defer upstream.Close()

	env := newAdminEnv(t)
	base := `{"name":"na","base_url":"` + upstream.URL + `/v1","api_key":"sk-k","quota_type":"new-api"`

	resp := env.call(t, http.MethodPost, "/api/channels", base+`}`)
	readJSON(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing quota_token: %d", resp.StatusCode)
	}

	resp = env.call(t, http.MethodPost, "/api/channels", base+`,"quota_token":"nap-token"}`)
	created := readJSON(t, resp)
	if resp.StatusCode != http.StatusCreated || created["quota_token"] != "nap-token" {
		t.Fatalf("create: %d %v", resp.StatusCode, created)
	}
	id := int64(created["id"].(float64))

	resp = env.call(t, http.MethodGet, "/api/channels", "")
	list := readJSON(t, resp)
	first := list["data"].([]any)[0].(map[string]any)
	if first["quota_token"] != "nap-token" {
		t.Errorf("list quota_token: %v", first)
	}

	resp = env.call(t, http.MethodGet, "/api/channels/"+itoa(id)+"/quota", "")
	got := readJSON(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("quota: %d %v", resp.StatusCode, got)
	}
	if gotAuth != "Bearer nap-token" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	data := got["data"].(map[string]any)
	if data["remaining"].(float64) != 6000000 || data["limit"].(float64) != 10000000 {
		t.Errorf("quota data: %v", data)
	}
	// 前端按 quota_per_unit 把 quota 折算成人民币（500000 quota = ¥1 → ¥12 / ¥20）
	if data["quota_per_unit"].(float64) != 500000 {
		t.Errorf("quota_per_unit: %v", data)
	}

	resp = env.call(t, http.MethodGet, "/api/quota-types", "")
	typesResp := readJSON(t, resp)
	var found bool
	for _, item := range typesResp["data"].([]any) {
		opt := item.(map[string]any)
		if opt["value"] == "new-api" {
			found = true
			if opt["requires_token"] != true {
				t.Errorf("new-api requires_token: %v", opt)
			}
		}
	}
	if !found {
		t.Errorf("new-api missing from quota types: %v", typesResp)
	}
}
