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

	"pengapi/internal/store"
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

func (p *OpenAI) endpoint(ch store.Channel, path string) string {
	return strings.TrimSuffix(ch.BaseURL, "/") + path
}

func (p *OpenAI) Chat(ctx context.Context, ch store.Channel, req ChatRequest) Result {
	r, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.endpoint(ch, "/chat/completions"), bytes.NewReader(req.Body))
	if err != nil {
		return Result{Err: err}
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+ch.APIKey)

	if !req.Stream {
		// 非流式：整体超时；body 在超时内读完，随后取消 ctx 无副作用
		tctx, cancel := context.WithTimeout(ctx, p.requestTimeout)
		defer cancel()
		resp, err := p.client.Do(r.WithContext(tctx))
		if err != nil {
			return Result{Err: err}
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		if err != nil {
			return Result{HTTPStatus: resp.StatusCode, Err: err}
		}
		return Result{HTTPStatus: resp.StatusCode, Header: resp.Header, Body: body}
	}

	// 流式：竞速实现首字节（响应头）超时；超时后取消 in-flight 请求
	r.Header.Set("Accept", "text/event-stream")
	rctx, cancel := context.WithCancel(ctx)
	type outcome struct {
		resp *http.Response
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		resp, err := p.client.Do(r.WithContext(rctx))
		done <- outcome{resp, err}
	}()
	timer := time.NewTimer(p.streamFirstByteTimeout)
	defer timer.Stop()
	select {
	case o := <-done:
		if o.err != nil {
			cancel()
			return Result{Err: o.err}
		}
		// 流式非 2xx：读掉错误 body，与非流式失败统一处理
		if o.resp.StatusCode < 200 || o.resp.StatusCode >= 300 {
			defer o.resp.Body.Close()
			defer cancel()
			body, _ := io.ReadAll(io.LimitReader(o.resp.Body, 1<<20))
			return Result{HTTPStatus: o.resp.StatusCode, Header: o.resp.Header, Body: body}
		}
		return Result{HTTPStatus: o.resp.StatusCode, Header: o.resp.Header,
			Stream: cancelReadCloser{ReadCloser: o.resp.Body, cancel: cancel}}
	case <-timer.C:
		cancel()
		return Result{Err: ErrStreamFirstByteTimeout}
	case <-ctx.Done():
		cancel()
		return Result{Err: ctx.Err()}
	}
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

func (p *OpenAI) ListModels(ctx context.Context, ch store.Channel) ([]UpstreamModel, error) {
	tctx, cancel := context.WithTimeout(ctx, p.requestTimeout)
	defer cancel()
	r, err := http.NewRequestWithContext(tctx, http.MethodGet, p.endpoint(ch, "/models"), nil)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+ch.APIKey)
	resp, err := p.client.Do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("upstream returned %d: %s", resp.StatusCode, truncate(string(body), 200))
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
