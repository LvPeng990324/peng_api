// Package httpx 收敛两类 HTTP 响应：/v1 的 OpenAI 风格错误与 /api 的管理端响应。
// auth、relay、admin 共用此包，避免各包重复实现错误格式。
package httpx

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
)

// MaxRequestBodyBytes 是管理端与 /v1 共用的请求体上限。
const MaxRequestBodyBytes = 10 << 20

// JSON 写出 JSON 响应。
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Error 写出管理端错误 {"error":"..."}。
func Error(w http.ResponseWriter, status int, msg string) {
	JSON(w, status, map[string]string{"error": msg})
}

// OpenAIError 写出客户端错误 {"error":{"message","type","code"}}。
func OpenAIError(w http.ResponseWriter, status int, msg string) {
	JSON(w, status, map[string]any{
		"error": map[string]any{"message": msg, "type": "api_error", "code": nil},
	})
}

// InternalError 记录服务端错误详情并按管理端格式返回 500，细节不回显给客户端。
func InternalError(w http.ResponseWriter, err error) {
	logInternal(err)
	Error(w, http.StatusInternalServerError, "internal error")
}

// InternalOpenAIError 同上，但按 /v1 的 OpenAI 格式返回。
func InternalOpenAIError(w http.ResponseWriter, err error) {
	logInternal(err)
	OpenAIError(w, http.StatusInternalServerError, "internal error")
}

func logInternal(err error) { log.Printf("http 500: %v", err) }

// DecodeAdminJSON 解析管理端请求体；失败时按管理端格式写出 400 并返回 false。
// /v1 不使用本函数（它的错误格式不同）。
func DecodeAdminJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, MaxRequestBodyBytes)).Decode(v); err != nil {
		Error(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return false
	}
	return true
}
