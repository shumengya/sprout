package openai

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
	"github.com/shumengya/sprout/internal/agent/config"
)

// Config 是构造 Provider 所需的信息，全部来自 models.jsonc。
type Config struct {
	Protocol   string // config.ProtocolChat 或 config.ProtocolResponses
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client // 可为 nil，使用带响应头超时的默认客户端
	Logger     *slog.Logger // 可为 nil
}

// New 按协议构造 Provider。
func New(cfg Config) (agent.Provider, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("API Key 为空")
	}
	c := &client{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:  cfg.APIKey,
		http:    cfg.HTTPClient,
		log:     cfg.Logger,
	}
	if c.http == nil {
		c.http = defaultHTTPClient()
	}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	switch cfg.Protocol {
	case config.ProtocolChat:
		return &chatProvider{c}, nil
	case config.ProtocolResponses:
		return &responsesProvider{c}, nil
	}
	return nil, fmt.Errorf("不支持的协议 %q", cfg.Protocol)
}

// defaultHTTPClient 不设整体超时（流式响应可能持续很久，取消靠 ctx），
// 只限制等待响应头的时间，避免服务端无响应时永远挂着。
func defaultHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 2 * time.Minute
	return &http.Client{Transport: tr}
}

// client 是两种协议共用的 HTTP 部分：鉴权、请求发送、错误归一化。
type client struct {
	baseURL string
	apiKey  string
	http    *http.Client
	log     *slog.Logger
}

// post 发送 JSON 请求。成功返回未读取的响应（调用方负责关闭 Body）；
// 非 2xx 返回 *agent.ProviderError；ctx 被取消返回 ctx.Err()。
func (c *client) post(ctx context.Context, path string, body any) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("编码请求失败: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", "sprout")

	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		c.log.Warn("provider request failed", "path", path, "error", err.Error())
		return nil, &agent.ProviderError{Retryable: true, Err: err}
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		pe := parseAPIError(resp.StatusCode, raw)
		c.log.Warn("provider http error", "path", path, "status", resp.StatusCode, "code", pe.Code, "message", pe.Message)
		return nil, pe
	}
	c.log.Debug("provider response", "path", path, "status", resp.StatusCode, "ttfb", time.Since(start))
	return resp, nil
}

// readErr 把读取流时的错误转成 Stream 应返回的错误：取消返回 ctx.Err()，其余视为连接中断。
func readErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return interrupted(err, "读取响应流时连接中断")
}
