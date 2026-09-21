package provider

import (
	"context"
	"errors"
	"io"
	"net/http"

	"pengapi/internal/store"
)

type ChatRequest struct {
	Model  string
	Body   []byte
	Stream bool
}

type Result struct {
	HTTPStatus int
	Header     http.Header
	Body       []byte
	Stream     io.ReadCloser
	Err        error
}

type UpstreamModel struct {
	ID              string `json:"id"`
	ContextLength   *int64 `json:"context_length"`
	MaxOutputTokens *int64 `json:"max_output_tokens"`
}

type Provider interface {
	Name() string
	Chat(ctx context.Context, ch store.Channel, req ChatRequest) Result
	ListModels(ctx context.Context, ch store.Channel) ([]UpstreamModel, error)
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
