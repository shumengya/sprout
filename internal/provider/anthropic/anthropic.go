// Package anthropic 实现 Anthropic Messages 协议（POST {baseUrl}/messages）的流式 Provider。
//
// 与其他协议的关键差异：
//   - 鉴权用 x-api-key 头，并必须带 anthropic-version 头。
//   - system 是顶层字段，不进 messages。
//   - 工具结果不是独立角色，而是 user 消息里的 tool_result block，且必须紧跟对应的 tool_use；
//     因此连续的 tool 消息要合并进同一条 user 消息。
//   - max_tokens 必填，未配置时给一个保守默认值。
//   - 流式事件以 SSE 的 event 名区分，data 里也有 type，两者一致。
package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/provider/transport"
)

const (
	// APIVersion 是 Anthropic 要求的版本头，与 v1 接口约定绑定。
	APIVersion = "2023-06-01"
	// defaultMaxTokens 用于 models.jsonc 没配 maxOutput 的情况。Anthropic 要求 max_tokens 必填。
	defaultMaxTokens = 8192
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

// provider 实现 POST {baseUrl}/messages 的流式调用。
type provider struct {
	transport *transport.Client
	apiKey    string
}

// Stream 实现 agent.Provider。
func (p *provider) Stream(ctx context.Context, req agent.Request, emit func(agent.Chunk) error) (agent.Response, error) {
	headers := http.Header{}
	headers.Set("x-api-key", p.apiKey)
	headers.Set("anthropic-version", APIVersion)
	resp, err := p.transport.Post(ctx, transport.RequestOptions{
		Path: "/messages", Headers: headers, ParseError: parseError,
	}, buildRequest(req))
	if err != nil {
		return agent.Response{}, err
	}
	defer resp.Body.Close()

	var (
		sse       = transport.NewReader(resp.Body)
		text      strings.Builder
		reasoning strings.Builder
		calls     = map[int]*callAcc{}
		blockKind = map[int]string{} // index -> content_block 类型
		stop      string
		usage     agent.Usage
		finished  bool
	)
	for !finished {
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
		var e streamEvent
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			return agent.Response{}, &agent.ProviderError{Message: "无法解析响应流: " + transport.ClipText(data, 200), Err: err}
		}
		switch e.Type {
		case "message_start":
			if e.Message != nil && e.Message.Usage != nil {
				usage.InputTokens = e.Message.Usage.InputTokens
			}
		case "content_block_start":
			blockKind[e.Index] = e.ContentBlock.Type
			if e.ContentBlock.Type == "tool_use" {
				calls[e.Index] = &callAcc{id: e.ContentBlock.ID, name: e.ContentBlock.Name}
			}
		case "content_block_delta":
			switch e.Delta.Type {
			case "text_delta":
				if e.Delta.Text != "" {
					text.WriteString(e.Delta.Text)
					if err := emit(agent.Chunk{TextDelta: e.Delta.Text}); err != nil {
						return agent.Response{}, err
					}
				}
			case "thinking_delta":
				if e.Delta.Thinking != "" {
					reasoning.WriteString(e.Delta.Thinking)
					if err := emit(agent.Chunk{ReasoningDelta: e.Delta.Thinking}); err != nil {
						return agent.Response{}, err
					}
				}
			case "input_json_delta":
				if c := calls[e.Index]; c != nil {
					c.args.WriteString(e.Delta.PartialJSON)
				}
			}
		case "message_delta":
			if e.Delta.StopReason != "" {
				stop = e.Delta.StopReason
			}
			if e.Usage != nil {
				usage.OutputTokens = e.Usage.OutputTokens
				usage.InputTokens = max(usage.InputTokens, e.Usage.InputTokens)
			}
		case "message_stop":
			finished = true
		case "error":
			code, msg := "", "模型生成失败"
			if e.Error != nil {
				code = e.Error.Type
				if e.Error.Message != "" {
					msg = e.Error.Message
				}
			}
			return agent.Response{}, transport.StreamError(resp.StatusCode, code, msg)
		}
	}

	if ctx.Err() != nil {
		return agent.Response{}, ctx.Err()
	}
	// 没有 message_stop：流被截断，不能把半截内容当成完整回答。
	if !finished {
		return agent.Response{}, transport.Interrupted(nil, "响应流在结束前中断")
	}
	if usage.InputTokens > 0 || usage.OutputTokens > 0 {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}

	out := collectCalls(calls)
	reason := agent.StopStop
	switch stop {
	case "max_tokens":
		reason = agent.StopLength
	case "tool_use":
		reason = agent.StopToolCalls
	}
	if stop == "tool_use" && len(out) == 0 {
		reason = agent.StopStop // 声称要调工具却没给出任何调用，交给内核继续
	}
	return agent.Response{
		Message: agent.Message{
			Role: agent.RoleAssistant, Content: []agent.Block{agent.TextBlock(text.String())}, Reasoning: reasoning.String(), ToolCalls: out,
		},
		Usage:      usage,
		StopReason: reason,
	}, nil
}

// callAcc 累积一个 tool_use block 的参数。
type callAcc struct {
	id, name string
	args     strings.Builder
}

// collectCalls 按 block index 的顺序返回调用。名字和参数都为空的槽位是残留，丢弃。
func collectCalls(m map[int]*callAcc) []agent.ToolCall {
	if len(m) == 0 {
		return nil
	}
	// index 数量很小，用计数排序避免额外依赖。
	maxIdx := -1
	for i := range m {
		maxIdx = max(maxIdx, i)
	}
	var out []agent.ToolCall
	for i := 0; i <= maxIdx; i++ {
		c := m[i]
		if c == nil {
			continue
		}
		if c.name == "" && c.args.Len() == 0 {
			continue
		}
		id := c.id
		if id == "" {
			id = transport.NewCallID()
		}
		out = append(out, agent.ToolCall{ID: id, Name: c.name, Arguments: c.args.String()})
	}
	return out
}

// parseError 解析 Anthropic 的错误体：{"type":"error","error":{"type","message"}}。
func parseError(status int, body []byte) *agent.ProviderError {
	var env struct {
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	pe := &agent.ProviderError{Status: status, Retryable: transport.RetryableStatus(status)}
	if json.Unmarshal(body, &env) == nil && env.Error != nil {
		pe.Message = env.Error.Message
		pe.Code = env.Error.Type
	}
	if pe.Message == "" {
		pe.Message = transport.ClipText(transport.TrimSpaceBytes(body), 500)
	}
	return pe
}
