package admin

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"pengapi/internal/httpx"
	"pengapi/internal/store"
)

// parseID 解析路径参数 {id}；失败时已写出 400，返回 ok=false。
func parseID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}

// loadChannel 解析 id 并读取渠道；404/500 已写出，ok=false 时调用方直接 return。
// id 直接取 ch.ID，不再作为冗余返回值。
func (h storeAPI) loadChannel(w http.ResponseWriter, r *http.Request) (*store.Channel, bool) {
	id, ok := parseID(w, r)
	if !ok {
		return nil, false
	}
	ch, err := h.store.GetChannel(r.Context(), id)
	if err != nil {
		httpx.InternalError(w, err)
		return nil, false
	}
	if ch == nil {
		httpx.Error(w, http.StatusNotFound, "channel not found")
		return nil, false
	}
	return ch, true
}
