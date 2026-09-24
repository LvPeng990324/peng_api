package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"pengapi/internal/relay/provider"
	"pengapi/internal/store"
)

// 客户端断开后日志仍要落盘，故 DB 操作用脱离请求生命周期的 ctx
func detachedCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func (e *Engine) insertLog(entry *store.LogEntry) {
	ctx, cancel := detachedCtx()
	defer cancel()
	if _, err := e.store.InsertLog(ctx, *entry); err != nil {
		log.Printf("insert request log: %v", err)
	}
}

func (e *Engine) run(w http.ResponseWriter, r *http.Request, tok *store.Token,
	mapping *store.Mapping, candidates []store.Candidate, body []byte, stream bool) {

	requestID := newUUID()
	var failures []string

	for i, cand := range candidates {
		p, ok := e.providers.For(cand.Channel.Type)
		if !ok {
			failures = append(failures, fmt.Sprintf("%s: unknown channel type %q", cand.Channel.Name, cand.Channel.Type))
			continue
		}

		// model 字段总是替换为模型实体名（客户端传的只是标准名）
		reqBody, err := replaceModel(body, cand.ModelName)
		if err != nil {
			writeOpenAIError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		entry := store.LogEntry{
			RequestID: requestID, Attempt: i + 1,
			TokenID: &tok.ID, TokenName: tok.Name,
			ModelRequested: mapping.Name, ModelCanonical: mapping.Name,
			ChannelID: &cand.Channel.ID, ChannelName: cand.Channel.Name,
			Stream: stream, RequestBody: string(reqBody),
		}
		// 客户端原始模型名单独保留（ModelRequested 语义）
		if orig := originalModelName(body); orig != "" {
			entry.ModelRequested = orig
		}

		start := time.Now()
		res := p.Chat(r.Context(), cand.Channel, provider.ChatRequest{
			Model: cand.ModelName, Body: reqBody, Stream: stream,
		})
		entry.LatencyMS = time.Since(start).Milliseconds()

		// 传输层失败
		if res.Err != nil {
			entry.Status = "failed"
			entry.Error = res.Err.Error()
			if r.Context().Err() != nil {
				entry.Error = "client aborted: " + res.Err.Error()
				e.insertLog(&entry)
				return // 客户端已走，只记日志，不再降级
			}
			e.insertLog(&entry)
			failures = append(failures, cand.Channel.Name+": "+res.Err.Error())
			continue
		}

		entry.HTTPStatus = &res.HTTPStatus

		// 成功
		if res.HTTPStatus >= 200 && res.HTTPStatus < 300 {
			if stream {
				e.finishStream(w, res.Stream, &entry)
				return
			}
			ct := res.Header.Get("Content-Type")
			if ct == "" {
				ct = "application/json"
			}
			w.Header().Set("Content-Type", ct)
			w.WriteHeader(res.HTTPStatus)
			w.Write(res.Body)
			entry.Status = "success"
			entry.ResponseBody = string(res.Body)
			entry.PromptTokens, entry.CompletionTokens, entry.PromptCacheHitTokens = parseUsage(res.Body)
			e.insertLog(&entry)
			return
		}

		entry.ResponseBody = string(res.Body)

		// 可降级的 HTTP 失败：429/5xx 试下一个模型
		if res.HTTPStatus == 429 || res.HTTPStatus >= 500 {
			entry.Status = "failed"
			entry.Error = fmt.Sprintf("upstream %d", res.HTTPStatus)
			e.insertLog(&entry)
			failures = append(failures, fmt.Sprintf("%s: upstream %d", cand.Channel.Name, res.HTTPStatus))
			continue
		}

		// 其余 4xx：透传，不降级
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(res.HTTPStatus)
		w.Write(res.Body)
		entry.Status = "failed"
		entry.Error = fmt.Sprintf("upstream %d (not retryable)", res.HTTPStatus)
		e.insertLog(&entry)
		return
	}

	writeOpenAIError(w, http.StatusServiceUnavailable, "all models failed: "+strings.Join(failures, "; "))
}

func originalModelName(body []byte) string {
	var m struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &m) != nil {
		return ""
	}
	return m.Model
}
