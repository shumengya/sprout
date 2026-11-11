// Package google 实现 Google Gemini 协议的流式 Provider
// （POST {baseUrl}/models/{model}:streamGenerateContent?alt=sse）。
//
// 与其他协议的关键差异：
//   - 模型名放在路径里，鉴权用 x-goog-api-key 头。
//   - role 只有 user / model，没有 system 角色；system 单独放 systemInstruction。
//   - 工具结果不是独立角色，而是 user 消息里的 functionResponse part。
//   - functionCall 没有 id，只有 name；回传 functionResponse 时按 name 对应。
//   - 没有 stop_reason 结尾事件，靠 finishReason 与流结束判断完成。
package google

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/provider/transport"
)

// Config 是构造 Provider 所需的信息，全部来自 models.jsonc。
type Config struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client // 可为 nil，使用带响应头超时的默认客户端
	Logger     *slog.Logger // 可为 nil
}

// New 构造 Provider。
func New(cfg Config) (agent.Provider, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("API Key 为空")
	}
	return &provider{&transport.Client{
		BaseURL: strings.TrimRight(cfg.BaseURL, "/"),
		HTTP:    cfg.HTTPClient,
		Logger:  cfg.Logger,
	}, cfg.APIKey}, nil
}

type provider struct {
	transport *transport.Client
	apiKey    string
}

// Stream 实现 agent.Provider。
func (p *provider) Stream(ctx context.Context, req agent.Request, emit func(agent.Chunk) error) (agent.Response, error) {
	headers := http.Header{}
	headers.Set("x-goog-api-key", p.apiKey)
	resp, err := p.transport.Post(ctx, transport.RequestOptions{
		Path: streamPath(req.Model), Headers: headers, ParseError: parseError, Extra: req.Thinking.Extra,
	}, buildRequest(req))
	if err != nil {
		return agent.Response{}, err
	}
	defer resp.Body.Close()

	var (
		sse       = transport.NewReader(resp.Body)
		text      strings.Builder
		reasoning strings.Builder
		calls     []agent.ToolCall
		usage     agent.Usage
		finish    string
		blocked   string // promptFeedback.blockReason
	)
	for {
		ev, err := sse.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return agent.Response{}, transport.ReadErr(ctx, err)
		}
		data := strings.TrimSpace(ev.Data)
		if data == "" || data == "[DONE]" {
			continue
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return agent.Response{}, &agent.ProviderError{Message: "无法解析响应流: " + transport.ClipText(data, 200), Err: err}
		}
		if chunk.Error != nil {
			return agent.Response{}, transport.StreamError(resp.StatusCode, chunk.Error.Status, chunk.Error.Message)
		}
		if pf := chunk.PromptFeedback; pf != nil && pf.BlockReason != "" {
			blocked = pf.BlockReason
			if pf.BlockReasonMessage != "" {
				blocked += "：" + pf.BlockReasonMessage
			}
		}
		if u := chunk.UsageMetadata; u != nil {
			usage = agent.Usage{
				InputTokens: u.PromptTokenCount, OutputTokens: u.CandidatesTokenCount,
				TotalTokens: u.TotalTokenCount, ReasoningTokens: u.ThoughtsTokenCount,
			}
		}
		for _, cand := range chunk.Candidates {
			if cand.FinishReason != "" {
				finish = cand.FinishReason
			}
			if cand.Content == nil {
				continue
			}
			for _, part := range cand.Content.Parts {
				// thought=true 的 part 是思维摘要（部分模型会带出来），不进正文。
				if part.Thought && part.Text != "" {
					reasoning.WriteString(part.Text)
					if err := emit(agent.Chunk{ReasoningDelta: part.Text}); err != nil {
						return agent.Response{}, err
					}
					continue
				}
				if part.Text != "" {
					text.WriteString(part.Text)
					if err := emit(agent.Chunk{TextDelta: part.Text}); err != nil {
						return agent.Response{}, err
					}
				}
				if part.FunctionCall != nil {
					args := string(part.FunctionCall.Args)
					if args == "" {
						args = "{}"
					}
					calls = append(calls, agent.ToolCall{
						ID:        newCallID(part.FunctionCall.Name),
						Name:      part.FunctionCall.Name,
						Arguments: args,
					})
				}
			}
		}
	}

	if ctx.Err() != nil {
		return agent.Response{}, ctx.Err()
	}
	if blocked != "" {
		return agent.Response{}, &agent.ProviderError{Status: resp.StatusCode, Code: "content_blocked", Message: "请求被 Gemini 拦截：" + blocked}
	}
	// 既没有 finishReason 也没有任何候选：流被中途截断。
	if finish == "" && text.Len() == 0 && len(calls) == 0 && reasoning.Len() == 0 {
		return agent.Response{}, transport.Interrupted(nil, "响应流在结束前中断")
	}

	stop := agent.StopStop
	switch {
	case len(calls) > 0:
		stop = agent.StopToolCalls
	case finish == "MAX_TOKENS":
		stop = agent.StopLength
	}
	return agent.Response{
		Message: agent.Message{
			Role: agent.RoleAssistant, Content: []agent.Block{agent.TextBlock(text.String())}, Reasoning: reasoning.String(), ToolCalls: calls,
		},
		Usage:      usage,
		StopReason: stop,
	}, nil
}

// streamPath 拼出 {model}:streamGenerateContent?alt=sse。
// 模型名可能带 "models/" 前缀（配置时写了全名），统一去重。
func streamPath(model string) string {
	name := strings.TrimPrefix(model, "models/")
	return "/models/" + url.PathEscape(name) + ":streamGenerateContent?alt=sse"
}

// newCallID 生成带函数名的调用 id，便于日志与调试区分不同调用。
func newCallID(name string) string {
	return transport.NewCallID() + "_" + name
}

// parseError 解析 Gemini 的错误体：{"error":{"code","message","status"}}。
func parseError(status int, body []byte) *agent.ProviderError {
	var env struct {
		Error *struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	pe := &agent.ProviderError{Status: status, Retryable: transport.RetryableStatus(status)}
	if json.Unmarshal(body, &env) == nil && env.Error != nil {
		pe.Message = env.Error.Message
		pe.Code = env.Error.Status
	}
	if pe.Message == "" {
		pe.Message = transport.ClipText(transport.TrimSpaceBytes(body), 500)
	}
	return pe
}
