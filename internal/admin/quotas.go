package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
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
	// QuotaPerUnit 是 quota 单位与货币单位的换算比例（new-api 默认 500000 quota = ¥1）；
	// 与 used/limit/remaining 一起下发给前端换算展示，原始 quota 数值不在这里折算。
	QuotaPerUnit *int64 `json:"quota_per_unit,omitempty"`
}

type quotaTypeOption struct {
	Value         string `json:"value"`
	Label         string `json:"label"`
	RequiresToken bool   `json:"requires_token"`
}

// quotaRequest 是一次额度查询的输入。固定公开域名的类型只用 APIKey；
// 自建网关（new-api）要用渠道的 quota_token 鉴权，并从 base_url 推导接口地址。
type quotaRequest struct {
	BaseURL string
	APIKey  string
	Token   string
}

type quotaFetcher func(ctx context.Context, client *http.Client, url string, in quotaRequest) (*channelQuota, error)

type quotaSource struct {
	Label string
	// URL 是固定接口地址；为空时改用 urlFrom 从渠道 base_url 推导，两者二选一。
	URL string
	// urlFrom 供自建网关这类域名随渠道变化的类型推导接口地址。
	urlFrom func(baseURL string) (string, error)
	// requiresToken 表示用渠道的 quota_token 而非 API Key 鉴权。
	requiresToken bool
	Fetch         quotaFetcher
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
	"new-api": {
		Label:         "New API",
		urlFrom:       newAPIURL,
		requiresToken: true,
		Fetch:         fetchNewAPI,
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
func (s *quotaService) urlFor(name, baseURL string) (string, error) {
	if u, ok := s.urlOverride[name]; ok {
		return u, nil
	}
	src := quotaSources[name]
	if src.URL != "" {
		return src.URL, nil
	}
	return src.urlFrom(baseURL)
}

func (s *quotaService) setURL(name, url string) { s.urlOverride[name] = url }

func (s *quotaService) fetch(ctx context.Context, quotaType string, in quotaRequest) (*channelQuota, error) {
	src, ok := quotaSources[quotaType]
	if !ok {
		return nil, fmt.Errorf("unsupported quota type %q", quotaType)
	}
	url, err := s.urlFor(quotaType, in.BaseURL)
	if err != nil {
		return nil, err
	}
	return src.Fetch(ctx, s.client, url, in)
}

func (s *quotaService) types() []quotaTypeOption {
	names := make([]string, 0, len(quotaSources))
	for name := range quotaSources {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]quotaTypeOption, 0, len(names))
	for _, name := range names {
		out = append(out, quotaTypeOption{
			Value: name, Label: quotaSources[name].Label, RequiresToken: quotaSources[name].requiresToken,
		})
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

// validateQuotaConfig 在保存渠道时挡住额度配置错误（未知类型、缺所需令牌），
// 不推迟到查询额度时才失败。
func validateQuotaConfig(quotaType, token string) string {
	if !validQuotaType(quotaType) {
		return "invalid quota_type"
	}
	if quotaType != "" && quotaSources[quotaType].requiresToken && strings.TrimSpace(token) == "" {
		return "quota_token is required for " + quotaType
	}
	return ""
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

func fetchOpenRouter(ctx context.Context, client *http.Client, url string, in quotaRequest) (*channelQuota, error) {
	var body struct {
		Data struct {
			FreeModelDailyRequests struct {
				Used      *int64 `json:"used"`
				Limit     *int64 `json:"limit"`
				Remaining *int64 `json:"remaining"`
			} `json:"free_model_daily_requests"`
		} `json:"data"`
	}
	if err := getQuota(ctx, client, url, in.APIKey, &body); err != nil {
		return nil, err
	}
	return &channelQuota{
		Used:      body.Data.FreeModelDailyRequests.Used,
		Limit:     body.Data.FreeModelDailyRequests.Limit,
		Remaining: body.Data.FreeModelDailyRequests.Remaining,
	}, nil
}

func fetchDeepSeek(ctx context.Context, client *http.Client, url string, in quotaRequest) (*channelQuota, error) {
	var body struct {
		BalanceInfos []struct {
			TotalBalance *string `json:"total_balance"`
		} `json:"balance_infos"`
	}
	if err := getQuota(ctx, client, url, in.APIKey, &body); err != nil {
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

func fetchKimi(ctx context.Context, client *http.Client, url string, in quotaRequest) (*channelQuota, error) {
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
	if err := getQuota(ctx, client, url, in.APIKey, &body); err != nil {
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

const newAPIQuotaPath = "/api/subscription/self"

// newAPIQuotaPerUnit 对应 new-api 的 QuotaPerUnit：500000 quota = ¥1。
// 金额换算放在前端展示层做，原始 quota 数值保持原样下发。
const newAPIQuotaPerUnit int64 = 500000

// newAPIURL 从渠道 base_url 推导额度接口：new-api 是自建网关，域名随渠道变化，
// base_url 一般带 /v1 之类前缀，因此只取到 scheme://host 再拼固定路径。
func newAPIURL(baseURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid base_url %q: cannot derive new-api quota url", baseURL)
	}
	return u.Scheme + "://" + u.Host + newAPIQuotaPath, nil
}

type newAPISubscription struct {
	AmountTotal int64  `json:"amount_total"`
	AmountUsed  int64  `json:"amount_used"`
	Status      string `json:"status"`
}

// fetchNewAPI 汇总生效中的订阅：used/limit/remaining 是各订阅之和（quota 单位），
// 没有生效订阅视为配置/账号问题，直接报错。
// quota_per_unit 一并返回，由前端换算成人民币展示。
func fetchNewAPI(ctx context.Context, client *http.Client, url string, in quotaRequest) (*channelQuota, error) {
	if strings.TrimSpace(in.Token) == "" {
		return nil, fmt.Errorf("new-api quota token not configured")
	}
	var body struct {
		Data struct {
			Subscriptions []struct {
				Subscription newAPISubscription `json:"subscription"`
			} `json:"subscriptions"`
		} `json:"data"`
	}
	if err := getQuota(ctx, client, url, strings.TrimSpace(in.Token), &body); err != nil {
		return nil, err
	}
	var used, limit int64
	active := 0
	for _, item := range body.Data.Subscriptions {
		sub := item.Subscription
		if sub.Status != "active" {
			continue
		}
		active++
		used += sub.AmountUsed
		limit += sub.AmountTotal
	}
	if active == 0 {
		return nil, fmt.Errorf("new-api returned no active subscription")
	}
	remaining := limit - used
	perUnit := newAPIQuotaPerUnit
	return &channelQuota{
		Used: &used, Limit: &limit, Remaining: &remaining, QuotaPerUnit: &perUnit,
	}, nil
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
	quota, err := h.svc.fetch(r.Context(), ch.QuotaType, quotaRequest{
		BaseURL: ch.BaseURL, APIKey: ch.APIKey, Token: ch.QuotaToken,
	})
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, "fetch quota: "+err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"data": quota})
}
