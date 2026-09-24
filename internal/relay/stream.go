package relay

import (
	"bufio"
	"bytes"
	"io"
	"net/http"

	"pengapi/internal/store"
)

// 透传上游 SSE 到客户端，累积全文；结束后落日志。
// 写出响应头后无法降级：客户端断开/上游断流都只影响日志，不再尝试下一模型。
func (e *Engine) finishStream(w http.ResponseWriter, src io.ReadCloser, entry *store.LogEntry) {
	defer src.Close()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		entry.Status = "failed"
		entry.Error = "streaming unsupported by response writer"
		e.insertLog(entry)
		return
	}

	var acc bytes.Buffer
	reader := bufio.NewReaderSize(src, 64<<10)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if _, werr := w.Write(line); werr != nil {
				// 客户端断开：只记日志
				entry.Status = "failed"
				entry.Error = "client disconnected: " + werr.Error()
				entry.ResponseBody = acc.String()
				e.insertLog(entry)
				return
			}
			flusher.Flush()
			acc.Write(line)
		}
		if err != nil {
			if err == io.EOF {
				entry.Status = "success"
				entry.ResponseBody = acc.String()
				entry.PromptTokens, entry.CompletionTokens, entry.PromptCacheHitTokens = parseUsageSSE(acc.String())
				e.insertLog(entry)
			} else {
				// 上游中途断流：已写出响应头，无法降级
				entry.Status = "failed"
				entry.Error = "stream interrupted: " + err.Error()
				entry.ResponseBody = acc.String()
				e.insertLog(entry)
			}
			return
		}
	}
}
