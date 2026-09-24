package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"pengapi/internal/store"

	"github.com/go-chi/chi/v5"
)

type mappingInput struct {
	Name            string  `json:"name"`
	ContextLength   *int64  `json:"context_length"`
	MaxOutputTokens *int64  `json:"max_output_tokens"`
	ModelIDs        []int64 `json:"model_ids"`
}

func (h *Handler) listMappings(w http.ResponseWriter, r *http.Request) {
	mappings, err := h.store.ListMappings(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": mappings})
}

func (h *Handler) createMapping(w http.ResponseWriter, r *http.Request) {
	var in mappingInput
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	mp, err := h.store.CreateMapping(r.Context(), in.Name, in.ContextLength, in.MaxOutputTokens, in.ModelIDs)
	if err != nil {
		writeMappingErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, mp)
}

// updateMapping 全量更新（含绑定 model_ids 整体替换）
func (h *Handler) updateMapping(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	var in mappingInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	err = h.store.UpdateMapping(r.Context(), id, strings.TrimSpace(in.Name),
		in.ContextLength, in.MaxOutputTokens, in.ModelIDs)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "mapping not found")
			return
		}
		writeMappingErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) deleteMapping(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.store.DeleteMapping(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func writeMappingErr(w http.ResponseWriter, err error) {
	switch {
	case strings.Contains(err.Error(), "UNIQUE"):
		writeErr(w, http.StatusBadRequest, "name already exists")
	case strings.Contains(err.Error(), "FOREIGN KEY"):
		writeErr(w, http.StatusBadRequest, "invalid model_ids")
	default:
		writeErr(w, http.StatusInternalServerError, "internal error: "+err.Error())
	}
}
