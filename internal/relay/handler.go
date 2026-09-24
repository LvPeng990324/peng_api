package relay

import (
	"encoding/json"
	"io"
	"net/http"

	"pengapi/internal/auth"
	"pengapi/internal/relay/provider"
	"pengapi/internal/store"
)

type Engine struct {
	store     *store.Store
	providers *provider.Registry
}

func NewEngine(st *store.Store, reg *provider.Registry) *Engine {
	return &Engine{store: st, providers: reg}
}

func (e *Engine) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	tok := auth.TokenFrom(r.Context())
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 10<<20))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var meta struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &meta); err != nil || meta.Model == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid request: model is required")
		return
	}
	// 标准名大小写敏感精确匹配
	mapping, err := e.store.ResolveMapping(r.Context(), meta.Model)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if mapping == nil {
		writeOpenAIError(w, http.StatusNotFound, "model not found: "+meta.Model)
		return
	}
	candidates, err := e.store.SelectModels(r.Context(), mapping.ID)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if len(candidates) == 0 {
		writeOpenAIError(w, http.StatusServiceUnavailable, "no available model: "+mapping.Name)
		return
	}
	e.run(w, r, tok, mapping, candidates, body, meta.Stream)
}

func (e *Engine) Models(w http.ResponseWriter, r *http.Request) {
	mappings, err := e.store.ListMappingsWithEnabledModels(r.Context())
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "internal error")
		return
	}
	data := make([]map[string]any, 0, len(mappings))
	for _, m := range mappings {
		item := map[string]any{
			"id": m.Name, "object": "model", "created": m.CreatedAt.Unix(), "owned_by": "peng-api",
		}
		if m.ContextLength != nil {
			item["context_length"] = *m.ContextLength
		}
		if m.MaxOutputTokens != nil {
			item["max_output_tokens"] = *m.MaxOutputTokens
		}
		data = append(data, item)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}
