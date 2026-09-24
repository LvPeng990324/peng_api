package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"pengapi/internal/relay/provider"
	"pengapi/internal/store"

	"github.com/go-chi/chi/v5"
)

type channelInput struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	BaseURL   string `json:"base_url"`
	APIKey    string `json:"api_key"`
	Priority  int    `json:"priority"`
	Enabled   *bool  `json:"enabled"`
	TestModel string `json:"test_model"`
}

func (in channelInput) validate() string {
	if strings.TrimSpace(in.Name) == "" {
		return "name is required"
	}
	if strings.TrimSpace(in.BaseURL) == "" {
		return "base_url is required"
	}
	if strings.TrimSpace(in.APIKey) == "" {
		return "api_key is required"
	}
	return ""
}

func (h *Handler) listChannels(w http.ResponseWriter, r *http.Request) {
	channels, err := h.store.ListChannels(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": channels})
}

func (h *Handler) createChannel(w http.ResponseWriter, r *http.Request) {
	var in channelInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	ch := store.Channel{
		Name: in.Name, Type: in.Type, BaseURL: in.BaseURL, APIKey: in.APIKey,
		Priority: in.Priority, TestModel: in.TestModel,
	}
	created, err := h.store.CreateChannel(r.Context(), ch)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *Handler) updateChannel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	existing, err := h.store.GetChannel(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if existing == nil {
		writeErr(w, http.StatusNotFound, "channel not found")
		return
	}
	var in channelInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	enabled := existing.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	ch := store.Channel{
		ID: id, Name: in.Name, Type: in.Type, BaseURL: in.BaseURL, APIKey: in.APIKey,
		Priority: in.Priority, Enabled: enabled, TestModel: in.TestModel,
	}
	if err := h.store.UpdateChannel(r.Context(), ch); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) deleteChannel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.store.DeleteChannel(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) toggleChannel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := h.store.SetChannelEnabled(r.Context(), id, in.Enabled); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type fetchModelsItem struct {
	Name            string `json:"name"` // 上游模型名
	ContextLength   *int64 `json:"context_length"`
	MaxOutputTokens *int64 `json:"max_output_tokens"`
	Exists          bool   `json:"exists"` // 是否已存在为该渠道下的模型实体
}

func (h *Handler) fetchModels(w http.ResponseWriter, r *http.Request) {
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
	p, ok := h.providers.For(ch.Type)
	if !ok {
		writeErr(w, http.StatusBadRequest, "unknown channel type: "+ch.Type)
		return
	}
	models, err := p.ListModels(r.Context(), *ch)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "fetch upstream models: "+err.Error())
		return
	}
	// 本渠道已有的模型实体名集合，用于标记上游列表中哪些已存在
	existing, err := h.store.ModelsOfChannel(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
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
	writeJSON(w, http.StatusOK, map[string]any{"data": items})
}

// addChannelModels 批量创建渠道下的模型实体：{names: [...]}，已存在的跳过（幂等）
func (h *Handler) addChannelModels(w http.ResponseWriter, r *http.Request) {
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
	var in struct {
		Names []string `json:"names"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if len(in.Names) == 0 {
		writeErr(w, http.StatusBadRequest, "names is empty")
		return
	}
	for _, name := range in.Names {
		if strings.TrimSpace(name) == "" {
			writeErr(w, http.StatusBadRequest, "name must not be empty")
			return
		}
	}
	for _, name := range in.Names {
		if _, err := h.store.CreateModel(r.Context(), id, name); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal error: "+err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) testChannel(w http.ResponseWriter, r *http.Request) {
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
	if ch.TestModel == "" {
		writeErr(w, http.StatusBadRequest, "test_model not configured: add a model entity first")
		return
	}
	p, ok := h.providers.For(ch.Type)
	if !ok {
		writeErr(w, http.StatusBadRequest, "unknown channel type: "+ch.Type)
		return
	}
	pingBody, _ := json.Marshal(map[string]any{
		"model":      ch.TestModel,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		"max_tokens": 1,
		"stream":     false,
	})
	start := time.Now()
	res := p.Chat(r.Context(), *ch, provider.ChatRequest{
		Model: ch.TestModel, Body: pingBody, Stream: false,
	})
	out := map[string]any{
		"latency_ms":   time.Since(start).Milliseconds(),
		"request_body": string(pingBody),
	}
	switch {
	case res.Err != nil:
		out["success"] = false
		out["error"] = res.Err.Error()
	case res.HTTPStatus >= 200 && res.HTTPStatus < 300:
		out["success"] = true
		out["http_status"] = res.HTTPStatus
		out["response_body"] = string(res.Body)
	default:
		out["success"] = false
		out["http_status"] = res.HTTPStatus
		out["response_body"] = string(res.Body)
		out["error"] = fmt.Sprintf("upstream returned %d", res.HTTPStatus)
	}
	writeJSON(w, http.StatusOK, out)
}
