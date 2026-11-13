package relay

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"time"

	"pengapi/internal/store"
)

// 透传上游 SSE 到客户端，累积全文并回填 entry；日志由调用方统一落盘。
// 写出响应头后无法降级：客户端断开/上游断流都只影响日志，不再尝试下一模型。
func finishStream(w http.ResponseWriter, src io.ReadCloser, entry *store.LogEntry, start time.Time) {
	defer src.Close()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	var acc bytes.Buffer
	// complete 是唯一的收口：回填总耗时、状态、响应体与用量。
	complete := func(status, errMsg string) {
		entry.LatencyMS = time.Since(start).Milliseconds()
		entry.Status = status
		entry.Error = errMsg
		entry.ResponseBody = acc.String()
		if status == store.LogStatusSuccess {
			entry.PromptTokens, entry.CompletionTokens, entry.PromptCacheHitTokens = parseUsageSSE(acc.String())
		}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		complete(store.LogStatusFailed, "streaming unsupported by response writer")
		return
	}

	reader := bufio.NewReaderSize(src, 64<<10)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if _, werr := w.Write(line); werr != nil {
				complete(store.LogStatusFailed, "client disconnected: "+werr.Error())
				return
			}
			flusher.Flush()
			acc.Write(line)
		}
		if err != nil {
			if err == io.EOF {
				complete(store.LogStatusSuccess, "")
			} else {
				complete(store.LogStatusFailed, "stream interrupted: "+err.Error())
			}
			return
		}
	}
}
