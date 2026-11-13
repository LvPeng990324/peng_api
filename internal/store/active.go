package store

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// ActiveAttempt 是一次正在转发、尚未落库的尝试。
// 它只存在于内存：进程崩溃时随进程消失，request_logs 里永远不会出现 processing 行。
type ActiveAttempt struct {
	Key              string
	RequestID        string
	Attempt          int
	TokenID          *int64
	TokenName        string
	Agent            string
	ModelRequested   string
	ModelCanonical   string
	ModelActual      string
	ChannelID        *int64
	ChannelName      string
	Stream           bool
	RequestBody      string
	StartedAt        time.Time
	CompletionTokens int // 流式近似已输出 token，未上报时为 0
}

// activeRegistry 是进程内进行中尝试的唯一存放点，读写都要过锁。
type activeRegistry struct {
	mu    sync.RWMutex
	items map[string]*ActiveAttempt
}

func (r *activeRegistry) begin(a ActiveAttempt) string {
	if a.Key == "" {
		a.Key = fmt.Sprintf("%s:%d", a.RequestID, a.Attempt)
	}
	if a.StartedAt.IsZero() {
		a.StartedAt = time.Now().UTC()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.items == nil {
		r.items = make(map[string]*ActiveAttempt)
	}
	item := a
	r.items[a.Key] = &item
	return a.Key
}

func (r *activeRegistry) setProgress(key string, tokens int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a, ok := r.items[key]; ok {
		a.CompletionTokens = tokens
	}
}

func (r *activeRegistry) end(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.items, key)
}

// snapshot 返回值拷贝，避免调用方在读锁外拿到被并发修改的字段。
func (r *activeRegistry) snapshot() []ActiveAttempt {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ActiveAttempt, 0, len(r.items))
	for _, a := range r.items {
		out = append(out, *a)
	}
	return out
}

func (r *activeRegistry) get(key string) (ActiveAttempt, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.items[key]
	if !ok {
		return ActiveAttempt{}, false
	}
	return *a, true
}

// NewActiveAttempt 从日志实体派生进行中尝试，避免调用点重复字段。
// 要求 e.CreatedAt 已设为请求开始时间。
func NewActiveAttempt(e LogEntry) ActiveAttempt {
	return ActiveAttempt{
		RequestID: e.RequestID, Attempt: e.Attempt,
		TokenID: e.TokenID, TokenName: e.TokenName, Agent: e.Agent,
		ModelRequested: e.ModelRequested, ModelCanonical: e.ModelCanonical, ModelActual: e.ModelActual,
		ChannelID: e.ChannelID, ChannelName: e.ChannelName,
		Stream: e.Stream, RequestBody: e.RequestBody, StartedAt: e.CreatedAt,
	}
}

// BeginAttempt 登记一次进行中的尝试，返回用于进度更新与详情定位的 key。
func (s *Store) BeginAttempt(a ActiveAttempt) string { return s.active.begin(a) }

// SetAttemptProgress 记录流式近似已输出 token；key 已结束时静默忽略。
func (s *Store) SetAttemptProgress(key string, tokens int) { s.active.setProgress(key, tokens) }

// EndAttempt 在尝试落库后释放内存行。
func (s *Store) EndAttempt(key string) { s.active.end(key) }

// ActiveAttempts 返回进行中的尝试，按开始时间倒序，并套用与 ListLogs 相同的筛选条件。
// 列表视图不带请求体；进度通过 live_completion_tokens 虚拟字段返回。
func (s *Store) ActiveAttempts(f LogFilter) []LogEntry {
	items := s.active.snapshot()
	out := make([]LogEntry, 0, len(items))
	for i := range items {
		if !f.matchActive(&items[i]) {
			continue
		}
		out = append(out, items[i].baseEntry())
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

// ActiveLog 返回单条进行中尝试的完整视图（含请求体），供详情弹窗使用。
func (s *Store) ActiveLog(key string) (*LogEntry, bool) {
	a, ok := s.active.get(key)
	if !ok {
		return nil, false
	}
	e := a.baseEntry()
	e.RequestBody = a.RequestBody
	return &e, true
}

// matchActive 在 Go 侧复刻 LogFilter.where 的语义（模糊匹配、相等、时间范围）。
// 唯一的差异点：进行中的行状态恒为 processing，筛选其他状态时直接排除。
func (f LogFilter) matchActive(a *ActiveAttempt) bool {
	if f.Status != "" && f.Status != LogStatusProcessing {
		return false
	}
	if f.Model != "" {
		needle := strings.ToLower(f.Model)
		if !containsFold(a.ModelRequested, needle) &&
			!containsFold(a.ModelCanonical, needle) &&
			!containsFold(a.ModelActual, needle) {
			return false
		}
	}
	if f.Agent != "" && !containsFold(a.Agent, strings.ToLower(f.Agent)) {
		return false
	}
	if f.TokenID != nil && (a.TokenID == nil || *a.TokenID != *f.TokenID) {
		return false
	}
	if f.ChannelID != nil && (a.ChannelID == nil || *a.ChannelID != *f.ChannelID) {
		return false
	}
	if f.StartTime != nil && a.StartedAt.Before(*f.StartTime) {
		return false
	}
	if f.EndTime != nil && a.StartedAt.After(*f.EndTime) {
		return false
	}
	return true
}

func containsFold(s, lowerNeedle string) bool {
	return strings.Contains(strings.ToLower(s), lowerNeedle)
}

func (a *ActiveAttempt) baseEntry() LogEntry {
	e := LogEntry{
		RequestID: a.RequestID, Attempt: a.Attempt,
		TokenID: a.TokenID, TokenName: a.TokenName, Agent: a.Agent,
		ModelRequested: a.ModelRequested, ModelCanonical: a.ModelCanonical, ModelActual: a.ModelActual,
		ChannelID: a.ChannelID, ChannelName: a.ChannelName,
		Stream: a.Stream, Status: LogStatusProcessing,
		CreatedAt: a.StartedAt, LiveKey: a.Key,
	}
	if a.CompletionTokens > 0 {
		n := a.CompletionTokens
		e.LiveCompletionTokens = &n
	}
	return e
}
