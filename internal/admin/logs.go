package admin

import (
	"net/http"
	"strconv"
	"time"

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
	httpx.JSON(w, http.StatusOK, map[string]any{
		"data": logs, "total": total, "page": f.Page, "size": f.Size,
	})

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
