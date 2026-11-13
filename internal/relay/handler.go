package relay

import (
	"io"
	"net/http"

	"pengapi/internal/auth"
	"pengapi/internal/httpx"
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
	if tok == nil {
		httpx.OpenAIError(w, http.StatusUnauthorized, "missing bearer token")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, httpx.MaxRequestBodyBytes))
	if err != nil {
		httpx.OpenAIError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req, err := parseRequestBody(raw)
	if err != nil {
		httpx.OpenAIError(w, http.StatusBadRequest, "invalid request: model is required")
		return
	}
	requestID, err := newUUID()
	if err != nil {
		httpx.InternalOpenAIError(w, err)
		return
	}
	// 标准名大小写敏感精确匹配
	mapping, err := e.store.ResolveMapping(r.Context(), req.model)
	if err != nil {
		httpx.InternalOpenAIError(w, err)
		return
	}
	if mapping == nil {
		httpx.OpenAIError(w, http.StatusNotFound, "model not found: "+req.model)
		return
	}
	candidates, err := e.store.SelectModels(r.Context(), mapping.ID)
	if err != nil {
		httpx.InternalOpenAIError(w, err)
		return
	}
	if len(candidates) == 0 {
		httpx.OpenAIError(w, http.StatusServiceUnavailable, "no available model: "+mapping.Name)
		return
	}
	e.run(&dispatch{
		w: w, r: r, tok: tok, mapping: mapping, candidates: candidates,
		req: req, requestID: requestID, agent: clientAgent(r),
	})
}

func (e *Engine) Models(w http.ResponseWriter, r *http.Request) {
	models, err := e.store.ListMappingsWithEnabledModels(r.Context())
	if err != nil {
		httpx.InternalOpenAIError(w, err)
		return
	}
	data := make([]map[string]any, 0, len(models))
	for _, m := range models {
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
	httpx.JSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}
