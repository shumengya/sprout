package openai

import (
	"encoding/json"

	"github.com/shumengya/sprout/internal/agent"
)

// Chat Completions 的线格式。只含本项目用到的字段。

type chatRequest struct {
	Model         string          `json:"model"`
	Messages      []chatMessage   `json:"messages"`
	Tools         []chatTool      `json:"tools,omitempty"`
	Stream        bool            `json:"stream"`
	StreamOptions *chatStreamOpts `json:"stream_options,omitempty"`
	MaxTokens     int             `json:"max_tokens,omitempty"`
}

type chatStreamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"` // 始终输出，哪怕是空串：部分兼容服务要求该字段存在
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatTool struct {
	Type     string          `json:"type"`
	Function chatToolDefFunc `json:"function"`
}

type chatToolDefFunc struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// buildChatRequest 把内部请求转换成 Chat Completions 请求：system 放 messages[0]，
// 工具结果作为 role:"tool" 消息，assistant 的 tool call 放在 tool_calls。
func buildChatRequest(req agent.Request) chatRequest {
	out := chatRequest{
		Model:         req.Model,
		Stream:        true,
		StreamOptions: &chatStreamOpts{IncludeUsage: true},
		MaxTokens:     req.MaxOutputTokens,
	}
	if req.System != "" {
		out.Messages = append(out.Messages, chatMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		cm := chatMessage{Role: string(m.Role), Content: m.Content}
		if m.Role == agent.RoleTool {
			cm.ToolCallID = m.ToolID
		}
		for _, tc := range m.ToolCalls {
			cm.ToolCalls = append(cm.ToolCalls, chatToolCall{
				ID: tc.ID, Type: "function", Function: chatFunction{Name: tc.Name, Arguments: tc.Arguments},
			})
		}
		out.Messages = append(out.Messages, cm)
	}
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, chatTool{
			Type:     "function",
			Function: chatToolDefFunc{Name: t.Name, Description: t.Description, Parameters: t.Parameters},
		})
	}
	return out
}

// chatChunk 是流式响应里的一个 data 事件。
type chatChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	// 部分兼容服务在流内用 error 字段报告失败。
	Error json.RawMessage `json:"error"`
}
