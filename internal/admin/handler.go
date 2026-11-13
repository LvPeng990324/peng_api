package admin

import (
	"github.com/go-chi/chi/v5"

	"pengapi/internal/auth"
	"pengapi/internal/relay/provider"
	"pengapi/internal/store"
)

// Handler 是管理端路由的组合根。只有真正需要额外依赖的资源才单独成类，
// 其余 CRUD 共用一个 dataAPI，避免为字段可见性造出一堆同构类型。
type Handler struct {
	auth     authAPI
	channels channelAPI
	data     dataAPI
	quotas   quotaAPI
}

type authAPI struct {
	sessions      *auth.SessionStore
	adminPassword string
}

// storeAPI 提供共享的库访问与 id / 渠道解析。
type storeAPI struct{ store *store.Store }

type dataAPI struct{ storeAPI }

type channelAPI struct {
	storeAPI
	providers *provider.Registry
}

type quotaAPI struct {
	storeAPI
	svc *quotaService
}

func New(st *store.Store, ss *auth.SessionStore, adminPassword string, reg *provider.Registry) *Handler {
	data := storeAPI{store: st}
	return &Handler{
		auth:     authAPI{sessions: ss, adminPassword: adminPassword},
		channels: channelAPI{storeAPI: data, providers: reg},
		data:     dataAPI{data},
		quotas:   quotaAPI{storeAPI: data, svc: newQuotaService()},
	}
}

func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/api", func(r chi.Router) {
		r.Post("/login", h.auth.login)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireSession(h.auth.sessions))
			r.Post("/logout", h.auth.logout)
			r.Get("/me", h.auth.me)
			r.Route("/models", func(r chi.Router) {
				r.Get("/", h.data.listModels)
				r.Post("/", h.data.createModel)
				r.Put("/{id}", h.data.updateModel)
				r.Delete("/{id}", h.data.deleteModel)
			})
			r.Route("/mappings", func(r chi.Router) {
				r.Get("/", h.data.listMappings)
				r.Post("/", h.data.createMapping)
				r.Put("/{id}", h.data.updateMapping)
				r.Delete("/{id}", h.data.deleteMapping)
			})
			r.Route("/tokens", func(r chi.Router) {
				r.Get("/", h.data.listTokens)
				r.Post("/", h.data.createToken)
				r.Get("/{id}", h.data.getToken)
				r.Put("/{id}", h.data.updateToken)
				r.Delete("/{id}", h.data.deleteToken)
			})
			r.Route("/channels", func(r chi.Router) {
				r.Get("/", h.channels.listChannels)
				r.Post("/", h.channels.createChannel)
				r.Put("/{id}", h.channels.updateChannel)
				r.Delete("/{id}", h.channels.deleteChannel)
				r.Get("/{id}/quota", h.quotas.getChannelQuota)
				r.Post("/{id}/toggle", h.channels.toggleChannel)
				r.Post("/{id}/fetch-models", h.channels.fetchModels)
				r.Post("/{id}/models", h.channels.addChannelModels)
				r.Post("/{id}/test", h.channels.testChannel)
			})
			r.Get("/quota-types", h.quotas.listQuotaTypes)
			r.Get("/logs", h.data.listLogs)
			r.Get("/logs/live/{key}", h.data.getLiveLog)
			r.Get("/logs/{id}", h.data.getLog)
		})
	})
}
