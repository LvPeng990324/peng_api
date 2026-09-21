package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"pengapi/internal/store"
)

type ctxKey int

const tokenCtxKey ctxKey = 0

func Bearer(st *store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			if !strings.HasPrefix(h, "Bearer ") {
				writeOpenAIError(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			tok, err := st.FindTokenByValue(r.Context(), strings.TrimPrefix(h, "Bearer "))
			if err != nil {
				writeOpenAIError(w, http.StatusInternalServerError, "internal error")
				return
			}
			if tok == nil {
				writeOpenAIError(w, http.StatusUnauthorized, "invalid token")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), tokenCtxKey, tok)))
		})
	}
}

func TokenFrom(ctx context.Context) *store.Token {
	t, _ := ctx.Value(tokenCtxKey).(*store.Token)
	return t
}

// 与 relay 包的错误格式保持一致（独立小函数避免 auth→relay 反向依赖）
func writeOpenAIError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": msg, "type": "api_error", "code": nil},
	})
}
