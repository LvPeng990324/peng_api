package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
)

// Endpoint 是协议层认识的唯一上游连接描述，不依赖持久层实体。
type Endpoint struct {
	BaseURL string
	APIKey  string
}

type ChatRequest struct {
	Model  string
	Body   []byte
	Stream bool
}

// Result 只承载 HTTP 层结果；传输层失败通过 error 返回。
type Result struct {
	HTTPStatus int
	Header     http.Header
	Body       []byte
	Stream     io.ReadCloser
}

func is2xx(status int) bool { return status >= 200 && status < 300 }

// OK 报告上游是否返回 2xx。
func (r Result) OK() bool { return is2xx(r.HTTPStatus) }

type UpstreamModel struct {
	ID              string `json:"id"`
	ContextLength   *int64 `json:"context_length"`
	MaxOutputTokens *int64 `json:"max_output_tokens"`
}

type Provider interface {
	Name() string
	Chat(ctx context.Context, ep Endpoint, req ChatRequest) (Result, error)
	ListModels(ctx context.Context, ep Endpoint) ([]UpstreamModel, error)
}

var ErrStreamFirstByteTimeout = errors.New("stream first byte timeout")

type Registry struct {
	m map[string]Provider
}

func NewRegistry(providers ...Provider) *Registry {
	r := &Registry{m: map[string]Provider{}}
	for _, p := range providers {
		r.m[p.Name()] = p
	}
	return r
}

func (r *Registry) For(typ string) (Provider, bool) {
	p, ok := r.m[typ]
	return p, ok
}

// Has 判断协议是否已注册，供管理端在保存渠道配置时做 fail-fast 校验。
func (r *Registry) Has(typ string) bool {
	_, ok := r.m[typ]
	return ok
}
