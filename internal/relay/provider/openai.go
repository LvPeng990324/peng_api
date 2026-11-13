package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	maxUpstreamBodyBytes  = 32 << 20
	maxUpstreamErrorBytes = 1 << 20
	maxModelsBodyBytes    = 4 << 20
	maxErrorSnippetBytes  = 200
)

type OpenAI struct {
	client                 *http.Client
	requestTimeout         time.Duration
	streamFirstByteTimeout time.Duration
}

func NewOpenAI(client *http.Client, requestTimeout, streamFirstByteTimeout time.Duration) *OpenAI {
	return &OpenAI{client: client, requestTimeout: requestTimeout, streamFirstByteTimeout: streamFirstByteTimeout}
}

func (p *OpenAI) Name() string { return "openai" }

func (p *OpenAI) endpoint(ep Endpoint, path string) string {
	return strings.TrimSuffix(ep.BaseURL, "/") + path
}

func (p *OpenAI) Chat(ctx context.Context, ep Endpoint, req ChatRequest) (Result, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.endpoint(ep, "/chat/completions"), bytes.NewReader(req.Body))
	if err != nil {
		return Result{}, err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+ep.APIKey)
	if !req.Stream {
		return p.chatNonStream(ctx, r)
	}
	return p.chatStream(ctx, r)
}

// chatNonStream 整体超时；body 在超时内读完，随后取消 ctx 无副作用。
func (p *OpenAI) chatNonStream(ctx context.Context, r *http.Request) (Result, error) {
	tctx, cancel := context.WithTimeout(ctx, p.requestTimeout)
	defer cancel()
	resp, err := p.client.Do(r.WithContext(tctx))
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamBodyBytes))
	if err != nil {
		return Result{HTTPStatus: resp.StatusCode}, err
	}
	return Result{HTTPStatus: resp.StatusCode, Header: resp.Header, Body: body}, nil
}

// chatStream 竞速实现首字节（响应头）超时；超时后取消 in-flight 请求并回收迟到响应。
func (p *OpenAI) chatStream(ctx context.Context, r *http.Request) (Result, error) {
	r.Header.Set("Accept", "text/event-stream")
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan chatOutcome, 1)
	go func() {
		resp, err := p.client.Do(r.WithContext(rctx))
		done <- chatOutcome{resp, err}
	}()
	timer := time.NewTimer(p.streamFirstByteTimeout)
	defer timer.Stop()
	select {
	case o := <-done:
		if o.err != nil {
			cancel()
			return Result{}, o.err
		}
		if !is2xx(o.resp.StatusCode) {
			defer o.resp.Body.Close()
			defer cancel()
			body, _ := io.ReadAll(io.LimitReader(o.resp.Body, maxUpstreamErrorBytes))
			return Result{HTTPStatus: o.resp.StatusCode, Header: o.resp.Header, Body: body}, nil
		}
		return Result{HTTPStatus: o.resp.StatusCode, Header: o.resp.Header,
			Stream: cancelReadCloser{ReadCloser: o.resp.Body, cancel: cancel}}, nil
	case <-timer.C:
		cancel()
		discardLateResponse(done)
		return Result{}, ErrStreamFirstByteTimeout
	case <-ctx.Done():
		cancel()
		discardLateResponse(done)
		return Result{}, ctx.Err()
	}
}

type chatOutcome struct {
	resp *http.Response
	err  error
}

// discardLateResponse 在超时/取消后回收可能迟到的响应，避免连接泄漏。
func discardLateResponse(done <-chan chatOutcome) {
	go func() {
		if o := <-done; o.resp != nil {
			o.resp.Body.Close()
		}
	}()
}

// Close 同时取消请求 ctx，释放底层连接资源
type cancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelReadCloser) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

func (p *OpenAI) ListModels(ctx context.Context, ep Endpoint) ([]UpstreamModel, error) {
	tctx, cancel := context.WithTimeout(ctx, p.requestTimeout)
	defer cancel()
	r, err := http.NewRequestWithContext(tctx, http.MethodGet, p.endpoint(ep, "/models"), nil)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+ep.APIKey)
	resp, err := p.client.Do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelsBodyBytes))
	if err != nil {
		return nil, err
	}
	if !is2xx(resp.StatusCode) {
		return nil, fmt.Errorf("upstream returned %d: %s", resp.StatusCode, truncate(string(body), maxErrorSnippetBytes))
	}
	var parsed struct {
		Data []UpstreamModel `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse models list: %w", err)
	}
	return parsed.Data, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
