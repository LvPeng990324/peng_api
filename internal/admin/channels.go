package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"pengapi/internal/httpx"
	"pengapi/internal/relay/provider"
	"pengapi/internal/store"
)

type channelInput struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	QuotaType  string `json:"quota_type"`
	QuotaToken string `json:"quota_token"`
	BaseURL    string `json:"base_url"`
	APIKey     string `json:"api_key"`
	Priority   int    `json:"priority"`
	Enabled    *bool  `json:"enabled"`
	Remark     string `json:"remark"`
}

// validate 在保存渠道时就把配置错误挡住：未知协议不应等到转发时才降级成 503。
// validType 由 provider.Registry.Has 提供，空 type 由 store 兜底为默认协议。
func (in channelInput) validate(validType func(string) bool) string {
	if strings.TrimSpace(in.Name) == "" {
		return "name is required"
	}
	if strings.TrimSpace(in.BaseURL) == "" {
		return "base_url is required"
	}
	if strings.TrimSpace(in.APIKey) == "" {
		return "api_key is required"
	}
	if in.Type != "" && !validType(in.Type) {
		return "unknown channel type: " + in.Type
	}
	if msg := validateQuotaConfig(in.QuotaType, in.QuotaToken); msg != "" {
		return msg
	}
	return ""
}

// toChannel 是输入 DTO → 领域实体的唯一转换点，创建与编辑共用。
func (in channelInput) toChannel(id int64, enabled bool) store.Channel {
	return store.Channel{
		ID: id, Name: in.Name, Type: in.Type, QuotaType: in.QuotaType, QuotaToken: in.QuotaToken,
		BaseURL: in.BaseURL, APIKey: in.APIKey, Priority: in.Priority,
		Enabled: enabled, Remark: in.Remark,
	}
}

func endpointOf(ch *store.Channel) provider.Endpoint {
	return provider.Endpoint{BaseURL: ch.BaseURL, APIKey: ch.APIKey}
}

func (h channelAPI) listChannels(w http.ResponseWriter, r *http.Request) {
	channels, err := h.store.ListChannels(r.Context())
	if err != nil {
		httpx.InternalError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"data": channels})
}

func (h channelAPI) createChannel(w http.ResponseWriter, r *http.Request) {
	var in channelInput
	if !httpx.DecodeAdminJSON(w, r, &in) {
		return
	}
	if msg := in.validate(h.providers.Has); msg != "" {
		httpx.Error(w, http.StatusBadRequest, msg)
		return
	}
	created, err := h.store.CreateChannel(r.Context(), in.toChannel(0, true))
	if err != nil {
		httpx.InternalError(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, created)
}

func (h channelAPI) updateChannel(w http.ResponseWriter, r *http.Request) {
	existing, ok := h.loadChannel(w, r)
	if !ok {
		return
	}
	var in channelInput
	if !httpx.DecodeAdminJSON(w, r, &in) {
		return
	}
	if msg := in.validate(h.providers.Has); msg != "" {
		httpx.Error(w, http.StatusBadRequest, msg)
		return
	}
	enabled := existing.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	if err := h.store.UpdateChannel(r.Context(), in.toChannel(existing.ID, enabled)); err != nil {
		httpx.InternalError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h channelAPI) deleteChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := h.store.DeleteChannel(r.Context(), id); err != nil {
		httpx.InternalError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h channelAPI) toggleChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if !httpx.DecodeAdminJSON(w, r, &in) {
		return
	}
	if err := h.store.SetChannelEnabled(r.Context(), id, in.Enabled); err != nil {
		httpx.InternalError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type fetchModelsItem struct {
	Name            string `json:"name"` // 上游模型名
	ContextLength   *int64 `json:"context_length"`
	MaxOutputTokens *int64 `json:"max_output_tokens"`
	Exists          bool   `json:"exists"` // 是否已存在为该渠道下的模型实体
}

func (h channelAPI) fetchModels(w http.ResponseWriter, r *http.Request) {
	ch, ok := h.loadChannel(w, r)
	if !ok {
		return
	}
	p, ok := h.providers.For(ch.Type)
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "unknown channel type: "+ch.Type)
		return
	}
	models, err := p.ListModels(r.Context(), endpointOf(ch))
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, "fetch upstream models: "+err.Error())
		return
	}
	// 本渠道已有的模型实体名集合，用于标记上游列表中哪些已存在
	existing, err := h.store.ModelsOfChannel(r.Context(), ch.ID)
	if err != nil {
		httpx.InternalError(w, err)
		return
	}
	exists := make(map[string]bool, len(existing))
	for _, m := range existing {
		exists[m.Name] = true
	}
	items := make([]fetchModelsItem, 0, len(models))
	for _, m := range models {
		items = append(items, fetchModelsItem{
			Name: m.ID, ContextLength: m.ContextLength,
			MaxOutputTokens: m.MaxOutputTokens, Exists: exists[m.ID],
		})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"data": items})
}

// addChannelModels 批量创建渠道下的模型实体：{names: [...]}，已存在的跳过（幂等）
func (h channelAPI) addChannelModels(w http.ResponseWriter, r *http.Request) {
	ch, ok := h.loadChannel(w, r)
	if !ok {
		return
	}
	var in struct {
		Names []string `json:"names"`
	}
	if !httpx.DecodeAdminJSON(w, r, &in) {
		return
	}
	if len(in.Names) == 0 {
		httpx.Error(w, http.StatusBadRequest, "names is empty")
		return
	}
	for _, name := range in.Names {
		if strings.TrimSpace(name) == "" {
			httpx.Error(w, http.StatusBadRequest, "name must not be empty")
			return
		}
	}
	for _, name := range in.Names {
		if _, err := h.store.CreateModel(r.Context(), ch.ID, name); err != nil {
			httpx.InternalError(w, err)
			return
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// testChannelInput 连通性测试的入参：目标模型与两条提示词都由前端弹窗指定，
// 不再依赖渠道上预先配置的测试模型。
type testChannelInput struct {
	Model        string `json:"model"`
	SystemPrompt string `json:"system_prompt"`
	UserPrompt   string `json:"user_prompt"`
}

func (h channelAPI) testChannel(w http.ResponseWriter, r *http.Request) {
	ch, ok := h.loadChannel(w, r)
	if !ok {
		return
	}
	var in testChannelInput
	if !httpx.DecodeAdminJSON(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Model) == "" {
		httpx.Error(w, http.StatusBadRequest, "model is required")
		return
	}
	if strings.TrimSpace(in.UserPrompt) == "" {
		httpx.Error(w, http.StatusBadRequest, "user_prompt is required")
		return
	}
	// 只允许测本渠道下已有的模型实体，避免拿任意模型名去打上游
	models, err := h.store.ModelsOfChannel(r.Context(), ch.ID)
	if err != nil {
		httpx.InternalError(w, err)
		return
	}
	known := false
	for _, m := range models {
		if m.Name == in.Model {
			known = true
			break
		}
	}
	if !known {
		httpx.Error(w, http.StatusBadRequest, "model not found in this channel: "+in.Model)
		return
	}
	p, ok := h.providers.For(ch.Type)
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "unknown channel type: "+ch.Type)
		return
	}
	messages := make([]map[string]string, 0, 2)
	if strings.TrimSpace(in.SystemPrompt) != "" {
		messages = append(messages, map[string]string{"role": "system", "content": in.SystemPrompt})
	}
	messages = append(messages, map[string]string{"role": "user", "content": in.UserPrompt})
	reqBody, err := json.Marshal(map[string]any{
		"model":    in.Model,
		"messages": messages,
		"stream":   false,
	})
	if err != nil {
		httpx.InternalError(w, err)
		return
	}
	start := time.Now()
	res, err := p.Chat(r.Context(), endpointOf(ch), provider.ChatRequest{
		Model: in.Model, Body: reqBody, Stream: false,
	})
	out := map[string]any{
		"latency_ms":   time.Since(start).Milliseconds(),
		"request_body": string(reqBody),
	}
	switch {
	case err != nil:
		out["success"] = false
		out["error"] = err.Error()
	case res.OK():
		out["success"] = true
		out["http_status"] = res.HTTPStatus
		out["response_body"] = string(res.Body)
	default:
		out["success"] = false
		out["http_status"] = res.HTTPStatus
		out["response_body"] = string(res.Body)
		out["error"] = fmt.Sprintf("upstream returned %d", res.HTTPStatus)
	}
	httpx.JSON(w, http.StatusOK, out)
}
