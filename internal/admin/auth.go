package admin

import (
	"crypto/subtle"
	"net/http"

	"pengapi/internal/auth"
	"pengapi/internal/httpx"
)

func (h authAPI) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if !httpx.DecodeAdminJSON(w, r, &body) {
		return
	}
	if subtle.ConstantTimeCompare([]byte(body.Password), []byte(h.adminPassword)) != 1 {
		httpx.Error(w, http.StatusUnauthorized, "invalid password")
		return
	}
	tok, err := h.sessions.Create()
	if err != nil {
		httpx.InternalError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookie,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	httpx.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h authAPI) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.SessionCookie); err == nil {
		h.sessions.Delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: auth.SessionCookie, Value: "", Path: "/", MaxAge: -1})
	httpx.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h authAPI) me(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}
