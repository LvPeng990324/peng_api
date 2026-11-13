package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"time"

	"pengapi/internal/httpx"
)

const (
	maxQuotaResponseBytes = 1 << 20
	maxQuotaErrorBytes    = 4 << 10
)

type channelQuota struct {
	Used      *int64   `json:"used,omitempty"`
	Limit     *int64   `json:"limit,omitempty"`
	Remaining *int64   `json:"remaining,omitempty"`
	Balance   *string  `json:"balance,omitempty"`
	FiveHour  *float64 `json:"five_hour,omitempty"`
	Weekly    *float64 `json:"weekly,omitempty"`
	Total     *float64 `json:"total,omitempty"`
}

type quotaTypeOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type quotaFetcher func(ctx context.Context, client *http.Client, url, apiKey string) (*channelQuota, error)

type quotaSource struct {
	Label string
	URL   string
	Fetch quotaFetcher
}

// quotaSources 是额度类型的唯一注册点：新增一种额度只需在此加一项，
// 校验白名单、分发逻辑和前端下拉都从这里派生。
var quotaSources = map[string]quotaSource{
	"openrouter": {
		Label: "OpenRouter",
		URL:   "https://openrouter.ai/api/v1/auth/key",
		Fetch: fetchOpenRouter,
	},
	"kimi": {
		Label: "Kimi",
		URL:   "https://api.kimi.com/coding/v1/usages",
		Fetch: fetchKimi,
	},
	"deepseek": {
		Label: "DeepSeek",
		URL:   "https://api.deepseek.com/user/balance",
		Fetch: fetchDeepSeek,
	},
}

type quotaService struct {
	client      *http.Client
	urlOverride map[string]string
}

func newQuotaService() *quotaService {
	return &quotaService{
		client:      &http.Client{Timeout: 10 * time.Second},
		urlOverride: make(map[string]string),
	}
}

// urlFor 默认取注册表中的 URL；只有测试需要指向假上游时才登记覆盖项，
// 避免把整份 URL 表复制进 service 形成两份可能漂移的状态。
func (s *quotaService) urlFor(name string) string {
	if u, ok := s.urlOverride[name]; ok {
		return u
	}
	return quotaSources[name].URL
}

func (s *quotaService) setURL(name, url string) { s.urlOverride[name] = url }

func (s *quotaService) fetch(ctx context.Context, quotaType, apiKey string) (*channelQuota, error) {
	src, ok := quotaSources[quotaType]
	if !ok {
		return nil, fmt.Errorf("unsupported quota type %q", quotaType)
	}
	return src.Fetch(ctx, s.client, s.urlFor(quotaType), apiKey)
}

func (s *quotaService) types() []quotaTypeOption {
	names := make([]string, 0, len(quotaSources))
	for name := range quotaSources {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]quotaTypeOption, 0, len(names))
	for _, name := range names {
		out = append(out, quotaTypeOption{Value: name, Label: quotaSources[name].Label})
	}
	return out
}

func validQuotaType(name string) bool {
	if name == "" {
		return true
	}
	_, ok := quotaSources[name]
	return ok
}

func getQuota(ctx context.Context, client *http.Client, url, apiKey string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxQuotaErrorBytes))
		return fmt.Errorf("quota upstream returned %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxQuotaResponseBytes)).Decode(out); err != nil {
		return fmt.Errorf("decode quota response: %w", err)
	}
	return nil
}

func fetchOpenRouter(ctx context.Context, client *http.Client, url, apiKey string) (*channelQuota, error) {
	var body struct {
		Data struct {
			FreeModelDailyRequests struct {
				Used      *int64 `json:"used"`
				Limit     *int64 `json:"limit"`
				Remaining *int64 `json:"remaining"`
			} `json:"free_model_daily_requests"`
		} `json:"data"`
	}
	if err := getQuota(ctx, client, url, apiKey, &body); err != nil {
		return nil, err
	}
	return &channelQuota{
		Used:      body.Data.FreeModelDailyRequests.Used,
		Limit:     body.Data.FreeModelDailyRequests.Limit,
		Remaining: body.Data.FreeModelDailyRequests.Remaining,
	}, nil
}

func fetchDeepSeek(ctx context.Context, client *http.Client, url, apiKey string) (*channelQuota, error) {
	var body struct {
		BalanceInfos []struct {
			TotalBalance *string `json:"total_balance"`
		} `json:"balance_infos"`
	}
	if err := getQuota(ctx, client, url, apiKey, &body); err != nil {
		return nil, err
	}
	if len(body.BalanceInfos) == 0 || body.BalanceInfos[0].TotalBalance == nil {
		return nil, fmt.Errorf("deepseek total_balance not found")
	}
	return &channelQuota{Balance: body.BalanceInfos[0].TotalBalance}, nil
}

type kimiRatio struct {
	UsedRatio *float64 `json:"used_ratio"`
}

type kimiCount struct {
	Limit     string `json:"limit"`
	Remaining string `json:"remaining"`
}

func fetchKimi(ctx context.Context, client *http.Client, url, apiKey string) (*channelQuota, error) {
	var body struct {
		Usage  *kimiCount `json:"usage"`
		Limits []struct {
			Window struct {
				Duration int    `json:"duration"`
				TimeUnit string `json:"timeUnit"`
			} `json:"window"`
			Detail kimiCount `json:"detail"`
		} `json:"limits"`
		Usages struct {
			FiveHour *kimiRatio `json:"limit_5h"`
			Weekly   *kimiRatio `json:"limit_7d"`
			Total    *kimiRatio `json:"limit_month_total"`
		} `json:"usages"`
	}
	if err := getQuota(ctx, client, url, apiKey, &body); err != nil {
		return nil, err
	}
	quota := &channelQuota{
		FiveHour: remainingPercent(body.Usages.FiveHour),
		Weekly:   remainingPercent(body.Usages.Weekly),
		Total:    remainingPercent(body.Usages.Total),
	}
	if quota.FiveHour == nil {
		for _, window := range body.Limits {
			if window.Window.Duration == 300 && window.Window.TimeUnit == "TIME_UNIT_MINUTE" {
				quota.FiveHour = countRemainingPercent(window.Detail)
				break
			}
		}
	}
	if quota.Weekly == nil && body.Usage != nil {
		quota.Weekly = countRemainingPercent(*body.Usage)
	}
	return quota, nil
}

func remainingPercent(entry *kimiRatio) *float64 {
	if entry == nil || entry.UsedRatio == nil {
		return nil
	}
	value := (1 - *entry.UsedRatio) * 100
	return &value
}

func countRemainingPercent(count kimiCount) *float64 {
	limit, err := strconv.ParseFloat(count.Limit, 64)
	if err != nil || limit <= 0 {
		return nil
	}
	remaining, err := strconv.ParseFloat(count.Remaining, 64)
	if err != nil {
		return nil
	}
	value := remaining / limit * 100
	return &value
}

func (h quotaAPI) listQuotaTypes(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]any{"data": h.svc.types()})
}

func (h quotaAPI) getChannelQuota(w http.ResponseWriter, r *http.Request) {
	ch, ok := h.loadChannel(w, r)
	if !ok {
		return
	}
	if ch.QuotaType == "" {
		httpx.Error(w, http.StatusBadRequest, "quota_type not configured")
		return
	}
	quota, err := h.svc.fetch(r.Context(), ch.QuotaType, ch.APIKey)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, "fetch quota: "+err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"data": quota})
}
