package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/shumengya/sprout/internal/agent"
)

// DefaultHTTPClient 不设整体超时（流式响应可能持续很久，取消靠 ctx），
// 只限制等待响应头的时间，避免服务端无响应时永远挂着。
func DefaultHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 2 * time.Minute
	return &http.Client{Transport: tr}
}

// Client 是发送一次 JSON POST 请求并归一化错误的小封装。
// 鉴权头由各协议自己在 RequestOptions 里提供（Bearer / x-api-key / x-goog-api-key 各不相同）。
type Client struct {
	BaseURL string // 已去掉结尾的 /
	HTTP    *http.Client
	Logger  *slog.Logger
}

func (c *Client) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.New(slog.DiscardHandler)
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return DefaultHTTPClient()
}

// RequestOptions 是一次请求的可变部分。Path 里的 query 由调用方自己拼好。
type RequestOptions struct {
	Path    string
	Headers http.Header
	// ParseError 把非 2xx 的响应体转成 ProviderError。为 nil 时用 ParseAPIError（OpenAI 风格）。
	ParseError func(status int, body []byte) *agent.ProviderError
}

// Post 发送 JSON 请求。成功返回未读取的响应（调用方负责关闭 Body）；
// 非 2xx 返回 *agent.ProviderError；ctx 被取消返回 ctx.Err()。
func (c *Client) Post(ctx context.Context, opts RequestOptions, body any) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("编码请求失败: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+opts.Path, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", "sprout")
	for k, v := range opts.Headers {
		req.Header[k] = v
	}

	log := c.logger()
	start := time.Now()
	resp, err := c.httpClient().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		log.Warn("provider request failed", "path", opts.Path, "error", err.Error())
		return nil, &agent.ProviderError{Retryable: true, Err: err}
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		parse := opts.ParseError
		if parse == nil {
			parse = ParseAPIError
		}
		pe := parse(resp.StatusCode, raw)
		pe.RetryAfter = ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		log.Warn("provider http error", "path", opts.Path, "status", resp.StatusCode, "code", pe.Code, "message", pe.Message)
		return nil, pe
	}
	log.Debug("provider response", "path", opts.Path, "status", resp.StatusCode, "ttfb", time.Since(start))
	return resp, nil
}

// ReadErr 把读取流时的错误转成 Stream 应返回的错误：取消返回 ctx.Err()，其余视为连接中断。
func ReadErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return Interrupted(err, "读取响应流时连接中断")
}

// ClipText 按字符截断，保证不切坏 UTF-8。
func ClipText(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// TrimSpaceBytes 便于把响应体转成可显示文本。保留在此避免各包重复写。
func TrimSpaceBytes(b []byte) string { return strings.TrimSpace(string(b)) }
