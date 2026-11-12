package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"pengapi/internal/store"

	"github.com/go-chi/chi/v5"
)

type modelInput struct {
	ChannelID  int64    `json:"channel_id"`
	Name       string   `json:"name"`
	MappingIDs *[]int64 `json:"mapping_ids"` // nil = 不动绑定；非 nil（含空数组）= 全量替换
}

func (h *Handler) listModels(w http.ResponseWriter, r *http.Request) {
	models, err := h.store.ListModels(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": models})
}

// createModel 创建模型实体；同渠道重名时幂等返回已存在实体
func (h *Handler) createModel(w http.ResponseWriter, r *http.Request) {
	var in modelInput
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.ChannelID == 0 {
		writeErr(w, http.StatusBadRequest, "channel_id is required")
		return
	}
	if in.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	// 创建是幂等的（重名返回已有实体），此时替换绑定会误伤已有模型；绑定只能在编辑时设置
	if in.MappingIDs != nil {
		writeErr(w, http.StatusBadRequest, "mapping_ids is only supported on update; edit the model to bind mappings")
		return
	}
	m, err := h.store.CreateModel(r.Context(), in.ChannelID, in.Name)
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY") {
			writeErr(w, http.StatusBadRequest, "channel not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

// updateModel 模型实体改名，并可同时全量替换该模型绑定的标准名（改名与绑定在同一事务内）
func (h *Handler) updateModel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	var in modelInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := h.store.UpdateModel(r.Context(), id, strings.TrimSpace(in.Name), in.MappingIDs); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeErr(w, http.StatusNotFound, "model not found")
		case strings.Contains(err.Error(), "UNIQUE"):
			writeErr(w, http.StatusBadRequest, "name already exists in this channel")
		case strings.Contains(err.Error(), "FOREIGN KEY"):
			writeErr(w, http.StatusBadRequest, "invalid mapping_ids")
		default:
			writeErr(w, http.StatusInternalServerError, "internal error")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) deleteModel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.store.DeleteModel(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
