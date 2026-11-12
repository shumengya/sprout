// Package anthropic 实现 Anthropic Messages 协议（POST {baseUrl}/messages）的流式 Provider。
//
// 与其他协议的关键差异：
//   - 鉴权用 x-api-key 头，并必须带 anthropic-version 头。
//   - system 是顶层字段，不进 messages。
//   - 工具结果不是独立角色，而是 user 消息里的 tool_result block，且必须紧跟对应的 tool_use；
//     因此连续的 tool 消息要合并进同一条 user 消息。
//   - max_tokens 必填，未配置时给一个保守默认值。
//   - 开启思考后，思考块带服务端签名，必须原样回传；本适配器把整段原始内容存进 Message.Native。
//   - 流式事件以 SSE 的 event 名区分，data 里也有 type，两者一致。
package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/config"
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
		Path: "/messages", Headers: headers, ParseError: parseError, Extra: req.Thinking.Extra,
	}, buildRequest(req))
	if err != nil {
		return agent.Response{}, err
	}
	defer resp.Body.Close()

	var (
		sse       = transport.NewReader(resp.Body)
		blocks    = map[int]*blockAcc{} // content_block 的 index → 累积状态
		text      strings.Builder
		reasoning strings.Builder
		stop      string
		usage     agent.Usage
		rawIn     int // Anthropic 报告的 input_tokens，不含缓存；结尾与缓存读写合并成 usage.InputTokens
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
				u := e.Message.Usage
				rawIn, usage.CacheReadTokens, usage.CacheWriteTokens = u.InputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens
			}
		case "content_block_start":
			cb := e.ContentBlock
			blocks[e.Index] = &blockAcc{kind: cb.Type, id: cb.ID, name: cb.Name, data: cb.Data}
			// 一条回答里可能有多段思考（工具调用之间的进度说明也是思考块），分段之间空一行。
			if cb.Type == "thinking" && reasoning.Len() > 0 {
				reasoning.WriteString("\n\n")
				if err := emit(agent.Chunk{ReasoningDelta: "\n\n"}); err != nil {
					return agent.Response{}, err
				}
			}
		case "content_block_delta":
			b := blocks[e.Index]
			if b == nil { // 没有 start 的增量：按 delta 类型补一个块，不丢内容
				b = &blockAcc{kind: deltaKind(e.Delta.Type)}
				blocks[e.Index] = b
			}
			switch e.Delta.Type {
			case "text_delta":
				if e.Delta.Text != "" {
					b.text.WriteString(e.Delta.Text)
					text.WriteString(e.Delta.Text)
					if err := emit(agent.Chunk{TextDelta: e.Delta.Text}); err != nil {
						return agent.Response{}, err
					}
				}
			case "thinking_delta":
				if e.Delta.Thinking != "" {
					b.text.WriteString(e.Delta.Thinking)
					reasoning.WriteString(e.Delta.Thinking)
					if err := emit(agent.Chunk{ReasoningDelta: e.Delta.Thinking}); err != nil {
						return agent.Response{}, err
					}
				}
			case "signature_delta":
				b.signature += e.Delta.Signature
			case "input_json_delta":
				b.text.WriteString(e.Delta.PartialJSON)
			}
		case "message_delta":
			if e.Delta.StopReason != "" {
				stop = e.Delta.StopReason
			}
			if e.Usage != nil {
				// message_delta 里的输入与缓存字段是累计值，只增不减；取大的那个。
				usage.OutputTokens = e.Usage.OutputTokens
				rawIn = max(rawIn, e.Usage.InputTokens)
				usage.CacheReadTokens = max(usage.CacheReadTokens, e.Usage.CacheReadInputTokens)
				usage.CacheWriteTokens = max(usage.CacheWriteTokens, e.Usage.CacheCreationInputTokens)
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
	// Anthropic 的 input_tokens 不含缓存；归一成“全部输入”的口径，缓存读写是其中的一部分。
	usage.InputTokens = rawIn + usage.CacheReadTokens + usage.CacheWriteTokens
	if usage.InputTokens > 0 || usage.OutputTokens > 0 {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}

	calls, native := assemble(blocks, req.Model)
	reason := agent.StopStop
	switch stop {
	case "max_tokens":
		reason = agent.StopLength
	case "tool_use":
		reason = agent.StopToolCalls
	}
	if stop == "tool_use" && len(calls) == 0 {
		reason = agent.StopStop // 声称要调工具却没给出任何调用，交给内核继续
	}
	return agent.Response{
		Message: agent.Message{
			Role: agent.RoleAssistant, Content: []agent.Block{agent.TextBlock(text.String())}, Reasoning: reasoning.String(),
			ToolCalls: calls, Native: native,
		},
		Usage:      usage,
		StopReason: reason,
	}, nil
}

// blockAcc 累积一个 content block。text 按块类型分别装正文、思考文本或工具参数 JSON。
type blockAcc struct {
	kind      string // text | thinking | redacted_thinking | tool_use | 其他
	text      strings.Builder
	signature string
	data      string // redacted_thinking 的加密内容
	id, name  string // tool_use
}

// deltaKind 推断缺少 content_block_start 时增量所属的块类型。
func deltaKind(deltaType string) string {
	switch deltaType {
	case "thinking_delta", "signature_delta":
		return "thinking"
	case "input_json_delta":
		return "tool_use"
	}
	return "text"
}

// assemble 按 block index 顺序整理出工具调用，以及需要原样回传的原始内容。
//
// 只有出现了思考块才保存原始内容：没有思考块时按内部消息重建就和原来一样，没必要多存一份。
// 原始内容里有本适配器复现不了的块（服务端工具等）或工具参数不是合法 JSON 时也不保存，
// 回退成按内部消息重建，宁可丢掉思考块，也不发出服务端无法接受的内容。
func assemble(blocks map[int]*blockAcc, model string) ([]agent.ToolCall, *agent.Native) {
	idxs := make([]int, 0, len(blocks))
	for i := range blocks {
		idxs = append(idxs, i)
	}
	slices.Sort(idxs)

	var (
		calls    []agent.ToolCall
		raw      []any
		thinking bool
		faithful = true
	)
	for _, i := range idxs {
		b := blocks[i]
		switch b.kind {
		case "text":
			// 服务端拒绝空的 text 块，没有内容的槽位不回传。
			if b.text.Len() > 0 {
				raw = append(raw, textBlock{Type: "text", Text: b.text.String()})
			}
		case "thinking":
			thinking = true
			raw = append(raw, thinkingBlock{Type: "thinking", Thinking: b.text.String(), Signature: b.signature})
		case "redacted_thinking":
			thinking = true
			raw = append(raw, redactedBlock{Type: "redacted_thinking", Data: b.data})
		case "tool_use":
			args := b.text.String()
			if b.name == "" && args == "" {
				continue // 名字和参数都为空的槽位是残留，丢弃
			}
			id := b.id
			if id == "" {
				id = transport.NewCallID()
			}
			calls = append(calls, agent.ToolCall{ID: id, Name: b.name, Arguments: args})
			input := json.RawMessage(args)
			if strings.TrimSpace(args) == "" {
				input = json.RawMessage("{}")
			}
			if !json.Valid(input) {
				faithful = false
			}
			raw = append(raw, toolUseBlock{Type: "tool_use", ID: id, Name: b.name, Input: input})
		default:
			faithful = false
		}
	}
	if !thinking || !faithful {
		return calls, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return calls, nil
	}
	return calls, &agent.Native{Protocol: config.ProtocolAnthropic, Model: model, Data: data}
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
