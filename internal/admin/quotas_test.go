package admin

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"pengapi/internal/store"
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
	svc.openRouterURL = upstream.URL + "/api/v1/auth/key"
	quota, err := svc.fetch(context.Background(), store.Channel{QuotaType: "openrouter", APIKey: "openrouter-key"})
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
	svc.deepSeekURL = upstream.URL + "/user/balance"
	quota, err := svc.fetch(context.Background(), store.Channel{QuotaType: "deepseek", APIKey: "deepseek-key"})
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
	svc.kimiURL = upstream.URL + "/coding/v1/usages"
	quota, err := svc.fetch(context.Background(), store.Channel{QuotaType: "kimi", APIKey: "kimi-key"})
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
	svc.kimiURL = upstream.URL
	quota, err := svc.fetch(context.Background(), store.Channel{QuotaType: "kimi", APIKey: "kimi-key"})
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
			svc.openRouterURL = upstream.URL
			if _, err := svc.fetch(context.Background(), store.Channel{QuotaType: "openrouter", APIKey: "bad"}); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestChannelQuotaAPI(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"free_model_daily_requests":{"used":58,"limit":100,"remaining":42}}}`))
	}))
	defer upstream.Close()

	env := newAdminEnv(t)
	env.h.quotas.openRouterURL = upstream.URL
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
