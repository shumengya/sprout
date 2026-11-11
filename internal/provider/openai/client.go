package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/config"
	"github.com/shumengya/sprout/internal/provider/transport"
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
		transport: &transport.Client{
			BaseURL: strings.TrimRight(cfg.BaseURL, "/"),
			HTTP:    cfg.HTTPClient,
			Logger:  cfg.Logger,
		},
		apiKey: cfg.APIKey,
	}
	switch cfg.Protocol {
	case config.ProtocolChat:
		return &chatProvider{c}, nil
	case config.ProtocolResponses:
		return &responsesProvider{c}, nil
	}
	return nil, fmt.Errorf("不支持的协议 %q", cfg.Protocol)
}

// client 是两种协议共用的 HTTP 部分：鉴权、请求发送、错误归一化。
type client struct {
	transport *transport.Client
	apiKey    string
}

// post 发送 JSON 请求，路径以 / 开头。成功返回未读取的响应（调用方负责关闭 Body）；
// 非 2xx 返回 *agent.ProviderError；ctx 被取消返回 ctx.Err()。
func (c *client) post(ctx context.Context, path string, body any, extra map[string]json.RawMessage) (*http.Response, error) {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+c.apiKey)
	return c.transport.Post(ctx, transport.RequestOptions{Path: path, Headers: headers, Extra: extra}, body)
}

// readErr 把读取流时的错误转成 Stream 应返回的错误：取消返回 ctx.Err()，其余视为连接中断。
func readErr(ctx context.Context, err error) error { return transport.ReadErr(ctx, err) }
