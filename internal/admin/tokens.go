package admin

import (
	"net/http"
	"strconv"

	"pengapi/internal/store"

	"github.com/go-chi/chi/v5"
)

type tokenRow struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Token     string `json:"token"` // 列表中打码
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at"`
}

func maskToken(t store.Token) tokenRow {
	masked := t.Token
	if len(masked) > 7 {
		masked = "sk-****" + masked[len(masked)-4:]
	}
	return tokenRow{ID: t.ID, Name: t.Name, Token: masked, Enabled: t.Enabled,
		CreatedAt: t.CreatedAt.Format("2006-01-02 15:04:05")}
}

func (h *Handler) listTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := h.store.ListTokens(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	rows := make([]tokenRow, 0, len(tokens))
	for _, t := range tokens {
		rows = append(rows, maskToken(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": rows})
}

func (h *Handler) createToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	t, err := h.store.CreateToken(r.Context(), in.Name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, t) // 完整 token 仅此一次返回
}

func (h *Handler) getToken(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	t, err := h.store.GetToken(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if t == nil {
		writeErr(w, http.StatusNotFound, "token not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": t.ID, "name": t.Name, "token": t.Token})
}

func (h *Handler) updateToken(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	var in struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := h.store.UpdateToken(r.Context(), id, in.Name, in.Enabled); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) deleteToken(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.store.DeleteToken(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
