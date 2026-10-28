package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"pengapi/internal/store"

	"github.com/go-chi/chi/v5"
)

const maxQuotaResponseBytes = 1 << 20

type channelQuota struct {
	Used      *int64   `json:"used,omitempty"`
	Limit     *int64   `json:"limit,omitempty"`
	Remaining *int64   `json:"remaining,omitempty"`
	Balance   *string  `json:"balance,omitempty"`
	FiveHour  *float64 `json:"five_hour,omitempty"`
	Weekly    *float64 `json:"weekly,omitempty"`
	Total     *float64 `json:"total,omitempty"`
}

type quotaService struct {
	client        *http.Client
	openRouterURL string
	kimiURL       string
	deepSeekURL   string
}

func newQuotaService() *quotaService {
	return &quotaService{
		client:        &http.Client{Timeout: 10 * time.Second},
		openRouterURL: "https://openrouter.ai/api/v1/auth/key",
		kimiURL:       "https://api.kimi.com/coding/v1/usages",
		deepSeekURL:   "https://api.deepseek.com/user/balance",
	}
}

func (s *quotaService) fetch(ctx context.Context, ch store.Channel) (*channelQuota, error) {
	switch ch.QuotaType {
	case "openrouter":
		return s.fetchOpenRouter(ctx, ch.APIKey)
	case "kimi":
		return s.fetchKimi(ctx, ch.APIKey)
	case "deepseek":
		return s.fetchDeepSeek(ctx, ch.APIKey)
	default:
		return nil, fmt.Errorf("unsupported quota type %q", ch.QuotaType)
	}
}

func (s *quotaService) get(ctx context.Context, url, apiKey string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("quota upstream returned %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxQuotaResponseBytes)).Decode(out); err != nil {
		return fmt.Errorf("decode quota response: %w", err)
	}
	return nil
}

func (s *quotaService) fetchOpenRouter(ctx context.Context, apiKey string) (*channelQuota, error) {
	var body struct {
		Data struct {
			FreeModelDailyRequests struct {
				Used      *int64 `json:"used"`
				Limit     *int64 `json:"limit"`
				Remaining *int64 `json:"remaining"`
			} `json:"free_model_daily_requests"`
		} `json:"data"`
	}
	if err := s.get(ctx, s.openRouterURL, apiKey, &body); err != nil {
		return nil, err
	}
	return &channelQuota{
		Used:      body.Data.FreeModelDailyRequests.Used,
		Limit:     body.Data.FreeModelDailyRequests.Limit,
		Remaining: body.Data.FreeModelDailyRequests.Remaining,
	}, nil
}

func (s *quotaService) fetchDeepSeek(ctx context.Context, apiKey string) (*channelQuota, error) {
	var body struct {
		BalanceInfos []struct {
			TotalBalance *string `json:"total_balance"`
		} `json:"balance_infos"`
	}
	if err := s.get(ctx, s.deepSeekURL, apiKey, &body); err != nil {
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

func (s *quotaService) fetchKimi(ctx context.Context, apiKey string) (*channelQuota, error) {
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
	if err := s.get(ctx, s.kimiURL, apiKey, &body); err != nil {
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

func (h *Handler) getChannelQuota(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	ch, err := h.store.GetChannel(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if ch == nil {
		writeErr(w, http.StatusNotFound, "channel not found")
		return
	}
	if ch.QuotaType == "" {
		writeErr(w, http.StatusBadRequest, "quota_type not configured")
		return
	}
	quota, err := h.quotas.fetch(r.Context(), *ch)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "fetch quota: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": quota})
}
