package openai

import (
	"encoding/json"

	"github.com/shumengya/sprout/internal/agent"
)

// Responses 的线格式。input 里的条目类型各不相同，分别定义结构体，
// 避免用一个带 omitempty 的大结构体导致空字符串字段被误丢。

type responsesRequest struct {
	Model           string          `json:"model"`
	Instructions    string          `json:"instructions,omitempty"`
	Input           []any           `json:"input"`
	Tools           []responsesTool `json:"tools,omitempty"`
	Stream          bool            `json:"stream"`
	Store           bool            `json:"store"` // 必须显式发 false，不能 omitempty
	MaxOutputTokens int             `json:"max_output_tokens,omitempty"`
}

type responsesMessage struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responsesFunctionCall struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type responsesFunctionOutput struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

// responsesTool 是扁平结构（不像 Chat 那样嵌套在 function 下）。
// strict 必须显式关掉：Responses 里函数工具默认 strict:true，会要求 schema 满足严格模式。
type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict"`
}

// buildResponsesRequest 把内部请求转换成 Responses 请求：system 放 instructions，
// 历史放 input[]，assistant 的 tool call 作为 function_call 条目，工具结果作为 function_call_output。
// store:false 配合完整 input，保持无服务端状态，和 Chat 每次带全量 messages 一致。
//
// function_call 条目不带 id：store:false 时服务端查不到对应的 reasoning 条目，
// 带上 fc_ id 反而可能触发“缺少配对 reasoning”的校验错误。
func buildResponsesRequest(req agent.Request) responsesRequest {
	out := responsesRequest{
		Model: req.Model, Instructions: req.System, Stream: true, Store: false,
		MaxOutputTokens: req.MaxOutputTokens, Input: []any{},
	}
	for _, m := range req.Messages {
		switch m.Role {
		case agent.RoleUser:
			out.Input = append(out.Input, responsesMessage{Type: "message", Role: "user", Content: m.Content})
		case agent.RoleAssistant:
			if m.Content != "" {
				out.Input = append(out.Input, responsesMessage{Type: "message", Role: "assistant", Content: m.Content})
			}
			for _, tc := range m.ToolCalls {
				out.Input = append(out.Input, responsesFunctionCall{
					Type: "function_call", CallID: tc.ID, Name: tc.Name, Arguments: tc.Arguments,
				})
			}
		case agent.RoleTool:
			out.Input = append(out.Input, responsesFunctionOutput{
				Type: "function_call_output", CallID: m.ToolID, Output: m.Content,
			})
		}
	}
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, responsesTool{
			Type: "function", Name: t.Name, Description: t.Description, Parameters: t.Parameters,
		})
	}
	return out
}

// responsesEvent 是流式响应里的一个事件，各事件类型共用此结构，按 Type 取用对应字段。
type responsesEvent struct {
	Type        string         `json:"type"`
	Delta       string         `json:"delta"`
	OutputIndex int            `json:"output_index"`
	ItemID      string         `json:"item_id"`
	Arguments   string         `json:"arguments"`
	Item        *responsesItem `json:"item"`
	Response    *responsesDone `json:"response"`
	// type == "error" 时的字段。
	Code    string `json:"code"`
	Message string `json:"message"`
}

type responsesItem struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Content   []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
}

// text 拼出 message 条目里的全部文本。
func (it responsesItem) text() string {
	var s string
	for _, c := range it.Content {
		s += c.Text + c.Refusal
	}
	return s
}

type responsesDone struct {
	Status string `json:"status"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		TotalTokens  int `json:"total_tokens"`
		// 思维链 token；不报告的服务没有这个字段。
		OutputTokensDetails *struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	} `json:"usage"`
	Output []responsesItem `json:"output"`
}
