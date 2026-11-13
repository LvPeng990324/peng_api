package admin

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"pengapi/internal/httpx"
	"pengapi/internal/store"
)

func (h dataAPI) listLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.LogFilter{
		Model:  q.Get("model"),
		Status: q.Get("status"),
		Agent:  q.Get("agent"),
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
	if v := q.Get("start_time"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.StartTime = &t
		}
	}
	if v := q.Get("end_time"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.EndTime = &t
		}
	}
	// 分页归一化只有一处实现，响应里的 page/size 即实际生效的值。
	f.Normalize()
	logs, total, err := h.store.ListLogs(r.Context(), f)
	if err != nil {
		httpx.InternalError(w, err)
		return
	}
	// 进行中的尝试只在内存里：仅首页附带，不占分页槽位、不计入 total。
	live := []store.LogEntry{}
	if f.Page == 1 {
		live = h.store.ActiveAttempts(f)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"data": logs, "live": live, "total": total, "page": f.Page, "size": f.Size,
	})

}

// getLiveLog 按 live_key 返回进行中尝试的完整视图（含请求体）。
func (h dataAPI) getLiveLog(w http.ResponseWriter, r *http.Request) {
	entry, ok := h.store.ActiveLog(chi.URLParam(r, "key"))
	if !ok {
		httpx.Error(w, http.StatusNotFound, "live log not found")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"data": entry})
}

func (h dataAPI) getLog(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	entry, err := h.store.GetLog(r.Context(), id)
	if err != nil {
		httpx.InternalError(w, err)
		return
	}
	if entry == nil {
		httpx.Error(w, http.StatusNotFound, "log not found")
		return
	}
	attempts, err := h.store.GetLogGroup(r.Context(), entry.RequestID)
	if err != nil {
		httpx.InternalError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"data": entry, "attempts": attempts})
}
