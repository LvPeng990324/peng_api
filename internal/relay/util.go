package relay

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

func writeOpenAIError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": msg, "type": "api_error", "code": nil},
	})
}

func replaceModel(body []byte, model string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	m["model"] = model
	return json.Marshal(m)
}

func newUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

type usage struct {
	PromptTokens         int  `json:"prompt_tokens"`
	CompletionTokens     int  `json:"completion_tokens"`
	PromptCacheHitTokens *int `json:"prompt_cache_hit_tokens"`
}

func parseUsage(body []byte) (prompt, completion, cacheHit *int) {
	var v struct {
		Usage *usage `json:"usage"`
	}
	if json.Unmarshal(body, &v) != nil || v.Usage == nil {
		return nil, nil, nil
	}
	return &v.Usage.PromptTokens, &v.Usage.CompletionTokens, v.Usage.PromptCacheHitTokens
}

func parseUsageSSE(text string) (prompt, completion, cacheHit *int) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			continue
		}
		if p, c, h := parseUsage([]byte(data)); p != nil {
			prompt, completion, cacheHit = p, c, h
		}
	}
	return
}
