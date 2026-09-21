package admin

import (
	"net/http"
	"strconv"

	"pengapi/internal/store"

	"github.com/go-chi/chi/v5"
)

func (h *Handler) listLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.LogFilter{
		Model:  q.Get("model"),
		Status: q.Get("status"),
	}
	f.Page, _ = strconv.Atoi(q.Get("page"))
	f.Size, _ = strconv.Atoi(q.Get("size"))
	if v := q.Get("token_id"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			f.TokenID = &id
		}
	}
	if v := q.Get("channel_id"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			f.ChannelID = &id
		}
	}
	logs, total, err := h.store.ListLogs(r.Context(), f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Size < 1 || f.Size > 200 {
		f.Size = 20
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": logs, "total": total, "page": f.Page, "size": f.Size,
	})
}

func (h *Handler) getLog(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	entry, err := h.store.GetLog(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if entry == nil {
		writeErr(w, http.StatusNotFound, "log not found")
		return
	}
	attempts, err := h.store.GetLogGroup(r.Context(), entry.RequestID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": entry, "attempts": attempts})
}
