package auth

import (
	"context"
	"net/http"
	"strings"

	"pengapi/internal/httpx"
	"pengapi/internal/store"
)

type ctxKey int

const tokenCtxKey ctxKey = 0

func Bearer(st *store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			if !strings.HasPrefix(h, "Bearer ") {
				httpx.OpenAIError(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			tok, err := st.FindTokenByValue(r.Context(), strings.TrimPrefix(h, "Bearer "))
			if err != nil {
				httpx.InternalOpenAIError(w, err)
				return
			}
			if tok == nil {
				httpx.OpenAIError(w, http.StatusUnauthorized, "invalid token")
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
