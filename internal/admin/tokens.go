package admin

import (
	"net/http"

	"pengapi/internal/httpx"
	"pengapi/internal/store"
)

// tokenView 是 Token 在管理端唯一的响应形状：列表用 maskedTokenView 打码，
// 创建与详情用 fullTokenView 返回完整值。DB 实体不再直接序列化。
type tokenView struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Token     string `json:"token"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at"`
}

func newTokenView(t store.Token, token string) tokenView {
	return tokenView{
		ID: t.ID, Name: t.Name, Token: token, Enabled: t.Enabled,
		CreatedAt: t.CreatedAt.Format("2006-01-02 15:04:05"),
	}
}

func maskedTokenView(t store.Token) tokenView {
	masked := t.Token
	if len(masked) > 7 {
		masked = "sk-****" + masked[len(masked)-4:]
	}
	return newTokenView(t, masked)
}

func fullTokenView(t store.Token) tokenView { return newTokenView(t, t.Token) }

func (h dataAPI) listTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := h.store.ListTokens(r.Context())
	if err != nil {
		httpx.InternalError(w, err)
		return
	}
	rows := make([]tokenView, 0, len(tokens))
	for _, t := range tokens {
		rows = append(rows, maskedTokenView(t))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"data": rows})
}

func (h dataAPI) createToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !httpx.DecodeAdminJSON(w, r, &in) {
		return
	}
	if in.Name == "" {
		httpx.Error(w, http.StatusBadRequest, "name is required")
		return
	}
	t, err := h.store.CreateToken(r.Context(), in.Name)
	if err != nil {
		httpx.InternalError(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, fullTokenView(*t)) // 完整 token 仅此一次返回
}

func (h dataAPI) getToken(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	t, err := h.store.GetToken(r.Context(), id)
	if err != nil {
		httpx.InternalError(w, err)
		return
	}
	if t == nil {
		httpx.Error(w, http.StatusNotFound, "token not found")
		return
	}
	httpx.JSON(w, http.StatusOK, fullTokenView(*t))
}

func (h dataAPI) updateToken(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var in struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if !httpx.DecodeAdminJSON(w, r, &in) {
		return
	}
	if err := h.store.UpdateToken(r.Context(), id, in.Name, in.Enabled); err != nil {
		httpx.InternalError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h dataAPI) deleteToken(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := h.store.DeleteToken(r.Context(), id); err != nil {
		httpx.InternalError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}
