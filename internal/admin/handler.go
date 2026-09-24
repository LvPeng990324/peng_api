package admin

import (
	"encoding/json"
	"io"
	"net/http"

	"pengapi/internal/auth"
	"pengapi/internal/relay/provider"
	"pengapi/internal/store"

	"github.com/go-chi/chi/v5"
)

type Handler struct {
	store         *store.Store
	sessions      *auth.SessionStore
	adminPassword string
	providers     *provider.Registry
}

func New(st *store.Store, ss *auth.SessionStore, adminPassword string, reg *provider.Registry) *Handler {
	return &Handler{store: st, sessions: ss, adminPassword: adminPassword, providers: reg}
}

func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/api", func(r chi.Router) {
		r.Post("/login", h.login)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireSession(h.sessions))
			r.Post("/logout", h.logout)
			r.Get("/me", h.me)
			r.Route("/models", func(r chi.Router) {
				r.Get("/", h.listModels)
				r.Post("/", h.createModel)
				r.Put("/{id}", h.updateModel)
				r.Delete("/{id}", h.deleteModel)
			})
			r.Route("/mappings", func(r chi.Router) {
				r.Get("/", h.listMappings)
				r.Post("/", h.createMapping)
				r.Put("/{id}", h.updateMapping)
				r.Delete("/{id}", h.deleteMapping)
			})
			r.Route("/tokens", func(r chi.Router) {
				r.Get("/", h.listTokens)
				r.Post("/", h.createToken)
				r.Get("/{id}", h.getToken)
				r.Put("/{id}", h.updateToken)
				r.Delete("/{id}", h.deleteToken)
			})
			r.Route("/channels", func(r chi.Router) {
				r.Get("/", h.listChannels)
				r.Post("/", h.createChannel)
				r.Put("/{id}", h.updateChannel)
				r.Delete("/{id}", h.deleteChannel)
				r.Post("/{id}/toggle", h.toggleChannel)
				r.Post("/{id}/fetch-models", h.fetchModels)
				r.Post("/{id}/models", h.addChannelModels)
				r.Post("/{id}/test", h.testChannel)
			})
			r.Get("/logs", h.listLogs)
			r.Get("/logs/{id}", h.getLog)
		})
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return false
	}
	return true
}
