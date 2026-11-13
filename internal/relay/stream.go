package relay

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"pengapi/internal/store"
)

// progressInterval 是流式进度写内存的节流间隔；前端 3s 轮询，1s 足够。
const progressInterval = time.Second

// 透传上游 SSE 到客户端，累积全文并回填 entry；日志由调用方统一落盘。
// 写出响应头后无法降级：客户端断开/上游断流都只影响日志，不再尝试下一模型。
func finishStream(w http.ResponseWriter, src io.ReadCloser, entry *store.LogEntry, start time.Time) {
	finishStreamWithProgress(w, src, entry, start, nil)
}

// finishStreamWithProgress 额外把流式近似已输出 token 通过 onProgress 上报：
// 循环内按 progressInterval 节流，收口时再报一次最终值（短流也有值）。
func finishStreamWithProgress(w http.ResponseWriter, src io.ReadCloser, entry *store.LogEntry, start time.Time, onProgress func(int)) {
	defer src.Close()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	var acc bytes.Buffer
	deltaCount := 0
	lastReport := time.Now() // 从首字开始计时，避免第一个 chunk 就上报一个极小的值
	// complete 是唯一的收口：回填总耗时、状态、响应体与用量，并上报最终近似值。
	complete := func(status, errMsg string) {
		entry.LatencyMS = time.Since(start).Milliseconds()
		entry.Status = status
		entry.Error = errMsg
		entry.ResponseBody = acc.String()
		if status == store.LogStatusSuccess {
			entry.PromptTokens, entry.CompletionTokens, entry.PromptCacheHitTokens = parseUsageSSE(acc.String())
		}
		if onProgress != nil {
			onProgress(deltaCount)
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
			if onProgress != nil {
				deltaCount += countStreamDelta(line)
				if time.Since(lastReport) >= progressInterval {
					onProgress(deltaCount)
					lastReport = time.Now()
				}
			}
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

// countStreamDelta 把一行 SSE 换算成近似 token 数：含非空 delta.content 或
// reasoning_content 的 chunk 记 1，同一 chunk 多个 choices 不重复计。
func countStreamDelta(line []byte) int {
	s := bytes.TrimSpace(line)
	if !bytes.HasPrefix(s, []byte("data:")) {
		return 0
	}
	payload := bytes.TrimSpace(s[len("data:"):])
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return 0
	}
	var v struct {
		Choices []struct {
			Delta struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal(payload, &v) != nil {
		return 0
	}
	for _, c := range v.Choices {
		if c.Delta.Content != "" || c.Delta.ReasoningContent != "" {
			return 1
		}
	}
	return 0
}
