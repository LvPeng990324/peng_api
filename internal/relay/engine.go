package relay

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"pengapi/internal/httpx"
	"pengapi/internal/relay/provider"
	"pengapi/internal/store"
)

const (
	logWriteTimeout = 5 * time.Second
	// 起始写落在请求关键路径上，超时要比收口写更短，避免拖慢首字
	logStartTimeout = 2 * time.Second
)

// 客户端断开后日志仍要落盘，故 DB 操作用脱离请求生命周期的 ctx
func detachedCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), logWriteTimeout)
}

// startLog 在发往上游之前写一行 processing，日志页立即可见。
// 写失败只记日志并返回 0，绝不阻断主请求；finishLog 会回落补写完整记录。
func (e *Engine) startLog(entry *store.LogEntry) int64 {
	entry.Status = store.LogStatusProcessing
	entry.CreatedAt = time.Now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), logStartTimeout)
	defer cancel()
	id, err := e.store.InsertLog(ctx, *entry)
	if err != nil {
		log.Printf("start request log: %v", err)
		return 0
	}
	return id
}

// finishLog 回填同一行的终态。panic 被 Recoverer 兜住时 defer 仍会执行，
// 此时 Status 可能为空或仍是 processing，两者都必须显式收口，
// 否则会留下没有终态的僵尸行。
func (e *Engine) finishLog(id int64, entry *store.LogEntry) {
	if entry.Status == "" || entry.Status == store.LogStatusProcessing {
		entry.Status = store.LogStatusFailed
		entry.Error = "interrupted: request did not finish"
	}
	entry.ID = id
	ctx, cancel := detachedCtx()
	defer cancel()
	if id == 0 {
		e.insertLog(ctx, entry)
		return
	}
	if err := e.store.UpdateLog(ctx, *entry); err != nil {
		log.Printf("update request log: %v", err)
		// 行被日志清理删掉时补写，避免丢日志
		if errors.Is(err, store.ErrNotFound) {
			e.insertLog(ctx, entry)
		}
	}
}

func (e *Engine) insertLog(ctx context.Context, entry *store.LogEntry) {
	if _, err := e.store.InsertLog(ctx, *entry); err != nil {
		log.Printf("insert request log: %v", err)
	}
}

// dispatch 收集一次客户端请求在整轮重试里不变的信息。
// 只有 run / tryCandidate 能看到它，辅助函数通过 attemptResult 返回结论。
type dispatch struct {
	w          http.ResponseWriter
	r          *http.Request
	tok        *store.Token
	mapping    *store.Mapping
	candidates []store.Candidate
	req        *requestBody
	requestID  string
	agent      string
	failures   []string
}

// attemptResult 是一次上游尝试对重试循环的输出，不携带写响应或改状态的能力。
type attemptResult struct {
	done    bool
	failure string
}

func (e *Engine) run(d *dispatch) {
	d.req.applyOverrides(d.mapping.ParameterOverrides)
	for i, cand := range d.candidates {
		result := e.tryCandidate(d, i, cand)
		if result.failure != "" {
			d.failures = append(d.failures, result.failure)
		}
		if result.done {
			return
		}
	}
	httpx.OpenAIError(d.w, http.StatusServiceUnavailable,
		"all models failed: "+strings.Join(d.failures, "; "))
}

// tryCandidate 处理一次上游尝试。日志在请求进入时先写一行 processing，
// 由 defer 统一回填终态：finishStream 是同步的，返回时 entry 已填完整，
// 因此流式与非流式走同一条收口路径。
func (e *Engine) tryCandidate(d *dispatch, attempt int, cand store.Candidate) attemptResult {
	p, ok := e.providers.For(cand.Channel.Type)
	if !ok {
		return attemptResult{failure: fmt.Sprintf("%s: unknown channel type %q", cand.Channel.Name, cand.Channel.Type)}
	}
	reqBody, err := d.req.bytesFor(cand.ModelName)
	if err != nil {
		httpx.OpenAIError(d.w, http.StatusBadRequest, "invalid request body")
		return attemptResult{done: true}
	}
	entry := store.LogEntry{
		RequestID: d.requestID, Attempt: attempt + 1,
		TokenID: &d.tok.ID, TokenName: d.tok.Name, Agent: d.agent,
		ModelRequested: d.req.model, ModelCanonical: d.mapping.Name, ModelActual: cand.ModelName,
		ChannelID: &cand.Channel.ID, ChannelName: cand.Channel.Name,
		Stream: d.req.isStream(), RequestBody: string(reqBody),
	}
	logID := e.startLog(&entry)
	defer e.finishLog(logID, &entry)

	start := time.Now()
	ep := provider.Endpoint{BaseURL: cand.Channel.BaseURL, APIKey: cand.Channel.APIKey}
	res, err := p.Chat(d.r.Context(), ep, provider.ChatRequest{
		Model: cand.ModelName, Body: reqBody, Stream: d.req.isStream(),
	})
	entry.LatencyMS = time.Since(start).Milliseconds()

	if err != nil {
		return transportFailure(d.r.Context(), &entry, cand, err)
	}
	entry.HTTPStatus = &res.HTTPStatus
	if res.OK() {
		if d.req.isStream() {
			// p.Chat 返回即收到上游响应头，此刻耗时即首字耗时。
			entry.FirstByteMS = entry.LatencyMS
			finishStream(d.w, res.Stream, &entry, start)
			return attemptResult{done: true}
		}
		writeSuccess(d.w, &entry, res)
		return attemptResult{done: true}
	}
	return httpFailure(d.w, &entry, cand, res)
}

func transportFailure(ctx context.Context, entry *store.LogEntry, cand store.Candidate, err error) attemptResult {
	entry.Status = store.LogStatusFailed
	entry.Error = err.Error()
	if ctx.Err() != nil {
		entry.Error = "client aborted: " + err.Error()
		return attemptResult{done: true} // 客户端已走，只记日志，不再降级
	}
	return attemptResult{failure: cand.Channel.Name + ": " + err.Error()}
}

func writeSuccess(w http.ResponseWriter, entry *store.LogEntry, res provider.Result) {
	ct := res.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(res.HTTPStatus)
	w.Write(res.Body)
	entry.Status = store.LogStatusSuccess
	entry.ResponseBody = string(res.Body)
	entry.PromptTokens, entry.CompletionTokens, entry.PromptCacheHitTokens = parseUsage(res.Body)
}

func httpFailure(w http.ResponseWriter, entry *store.LogEntry, cand store.Candidate, res provider.Result) attemptResult {
	entry.ResponseBody = string(res.Body)
	if res.HTTPStatus == 429 || res.HTTPStatus >= 500 {
		entry.Status = store.LogStatusFailed
		entry.Error = fmt.Sprintf("upstream %d", res.HTTPStatus)
		return attemptResult{failure: fmt.Sprintf("%s: upstream %d", cand.Channel.Name, res.HTTPStatus)}
	}
	// 其余 4xx：原样透传，不降级
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(res.HTTPStatus)
	w.Write(res.Body)
	entry.Status = store.LogStatusFailed
	entry.Error = fmt.Sprintf("upstream %d (not retryable)", res.HTTPStatus)
	return attemptResult{done: true}
}
