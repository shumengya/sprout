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
	// Reasoning 是思考设置，只在 models.jsonc 为当前档位声明了强度时才发。
	Reasoning *responsesReasoning `json:"reasoning,omitempty"`
}

type responsesReasoning struct {
	Effort string `json:"effort,omitempty"`
	// Summary 为 auto 时服务端在流里返回可读的思考摘要（reasoning_summary_text 事件）。
	Summary string `json:"summary,omitempty"`
}

type responsesMessage struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content any    `json:"content"` // 字符串或 input_text / input_image 内容数组
}

type responsesPart struct {
	Type     string `json:"type"` // input_text | input_image
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"` // data:image/png;base64,...
}

// responsesContent 把内部内容块转成 Responses 的 content。纯文本发字符串，
// 带图片时发内容数组（Responses 的图片只能走 input_image part）。
func responsesContent(m agent.Message) any {
	if !m.HasImages() {
		return m.Text()
	}
	parts := make([]responsesPart, 0, len(m.Content))
	for _, blk := range m.Content {
		if blk.Type == agent.BlockImage {
			parts = append(parts, responsesPart{Type: "input_image", ImageURL: dataURL(blk)})
			continue
		}
		parts = append(parts, responsesPart{Type: "input_text", Text: blk.Text})
	}
	return parts
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
	if t := req.Thinking; t.Effort != "" {
		out.Reasoning = &responsesReasoning{Effort: t.Effort}
		if t.Summary {
			out.Reasoning.Summary = "auto"
		}
	}
	// function_call_output 只放文本，工具结果的图片在这组输出之后用一条 user 消息补发（同 Chat）。
	var toolImages []agent.Block
	flushImages := func() {
		if len(toolImages) > 0 {
			blocks := append([]agent.Block{agent.TextBlock(toolImagesNote)}, toolImages...)
			out.Input = append(out.Input, responsesMessage{Type: "message", Role: "user", Content: responsesContent(agent.Message{Content: blocks})})
			toolImages = nil
		}
	}
	for _, m := range req.Messages {
		if m.Role != agent.RoleTool {
			flushImages()
		}
		switch m.Role {
		case agent.RoleUser:
			out.Input = append(out.Input, responsesMessage{Type: "message", Role: "user", Content: responsesContent(m)})
		case agent.RoleAssistant:
			if text := m.Text(); text != "" {
				out.Input = append(out.Input, responsesMessage{Type: "message", Role: "assistant", Content: text})
			}
			for _, tc := range m.ToolCalls {
				out.Input = append(out.Input, responsesFunctionCall{
					Type: "function_call", CallID: tc.ID, Name: tc.Name, Arguments: tc.Arguments,
				})
			}
		case agent.RoleTool:
			out.Input = append(out.Input, responsesFunctionOutput{
				Type: "function_call_output", CallID: m.ToolID, Output: m.Text(),
			})
			toolImages = append(toolImages, imageBlocks(m)...)
		}
	}
	flushImages()
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
	// reasoning_summary_part.added 的摘要序号，用来在多段摘要之间补空行。
	SummaryIndex int `json:"summary_index"`
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
	// Summary 是 reasoning 条目的可读摘要，流里没有发增量时用它补上。
	Summary []struct {
		Text string `json:"text"`
	} `json:"summary"`
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
		// 命中缓存的 token（已含在 input_tokens 里）；不报告的服务没有这个字段。
		InputTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"input_tokens_details"`
		// 思维链 token；不报告的服务没有这个字段。
		OutputTokensDetails *struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	} `json:"usage"`
	Output []responsesItem `json:"output"`
}
