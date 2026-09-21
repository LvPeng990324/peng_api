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
	Models    []struct {
		ModelID       int64  `json:"model_id"`
		UpstreamModel string `json:"upstream_model"`
	} `json:"models"`
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
	for _, b := range in.Models {
		ch.Models = append(ch.Models, store.ChannelModelBinding{ModelID: b.ModelID, UpstreamModel: b.UpstreamModel})
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
	for _, b := range in.Models {
		ch.Models = append(ch.Models, store.ChannelModelBinding{ModelID: b.ModelID, UpstreamModel: b.UpstreamModel})
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

func (h *Handler) resetChannel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.store.ResetChannelState(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type fetchModelsItem struct {
	UpstreamID       string `json:"upstream_id"`
	ContextLength    *int64 `json:"context_length"`
	MaxOutputTokens  *int64 `json:"max_output_tokens"`
	SuggestedModelID *int64 `json:"suggested_model_id"`
	SuggestedName    string `json:"suggested_name"`
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
	items := make([]fetchModelsItem, 0, len(models))
	for _, m := range models {
		item := fetchModelsItem{
			UpstreamID: m.ID, ContextLength: m.ContextLength,
			MaxOutputTokens: m.MaxOutputTokens, SuggestedName: m.ID,
		}
		if existing, err := h.store.ResolveModel(r.Context(), m.ID); err == nil && existing != nil {
			item.SuggestedModelID = &existing.ID
			item.SuggestedName = existing.Name
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items})
}

func (h *Handler) bindModels(w http.ResponseWriter, r *http.Request) {
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
		Bindings []store.BindingInput `json:"bindings"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if len(in.Bindings) == 0 {
		writeErr(w, http.StatusBadRequest, "bindings is empty")
		return
	}
	for _, b := range in.Bindings {
		if b.UpstreamModel == "" {
			writeErr(w, http.StatusBadRequest, "upstream_model is required")
			return
		}
	}
	if err := h.store.BindModels(r.Context(), id, in.Bindings); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error: "+err.Error())
		return
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
		writeErr(w, http.StatusBadRequest, "test_model not configured: pick a cheap bound model first")
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
