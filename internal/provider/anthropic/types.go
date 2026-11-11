package anthropic

import (
	"encoding/json"

	"github.com/shumengya/sprout/internal/agent"
)

// Messages 协议的线格式。只含本项目用到的字段。

type request struct {
	Model     string    `json:"model"`
	System    string    `json:"system,omitempty"`
	Messages  []message `json:"messages"`
	Tools     []tool    `json:"tools,omitempty"`
	Stream    bool      `json:"stream"`
	MaxTokens int       `json:"max_tokens"`
}

// message 的 content 是 block 数组。用 []any 装不同类型：
// text / tool_use / tool_result / thinking 各有独立结构。
type message struct {
	Role    string `json:"role"` // user | assistant
	Content []any  `json:"content"`
}

type textBlock struct {
	Type string `json:"type"` // "text"
	Text string `json:"text"`
}

type toolUseBlock struct {
	Type  string          `json:"type"` // "tool_use"
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type toolResultBlock struct {
	Type      string `json:"type"` // "tool_result"
	ToolUseID string `json:"tool_use_id"`
	Content   any    `json:"content"` // 纯文本发字符串，带图片时发 text / image block 数组
}

// imageBlock 是 base64 内联图片。Anthropic 也支持 url source，这里只用 base64，避免依赖外部可达性。
type imageBlock struct {
	Type   string      `json:"type"` // "image"
	Source imageSource `json:"source"`
}

type imageSource struct {
	Type      string `json:"type"` // "base64"
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// contentBlocks 把内部内容块转成 Anthropic 的 block 数组，顺序不变。
func contentBlocks(m agent.Message) []any {
	blocks := make([]any, 0, len(m.Content))
	for _, blk := range m.Content {
		if blk.Type == agent.BlockImage {
			blocks = append(blocks, imageBlock{Type: "image", Source: imageSource{
				Type: "base64", MediaType: blk.MediaType, Data: blk.Data,
			}})
			continue
		}
		blocks = append(blocks, textBlock{Type: "text", Text: blk.Text})
	}
	return blocks
}

type tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// buildRequest 把内部请求转换成 Messages 请求。
//
// 消息映射规则：
//   - user 消息 → user 消息，content 为 text block。
//   - assistant 消息 → assistant 消息，text 与 tool_use block 按顺序排列。
//   - tool 消息 → user 消息里的 tool_result block；连续多条 tool 消息合并进同一条 user 消息，
//     因为 tool_result 必须紧跟在对应 tool_use 之后，中间不能再插一条 user 消息。
func buildRequest(req agent.Request) request {
	maxTokens := req.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	out := request{
		Model: req.Model, System: req.System, Stream: true, MaxTokens: maxTokens,
		Messages: []message{},
	}
	for _, m := range req.Messages {
		switch m.Role {
		case agent.RoleUser:
			out.Messages = append(out.Messages, message{Role: "user", Content: contentBlocks(m)})
		case agent.RoleAssistant:
			var blocks []any
			// 不回传 Reasoning：thinking block 必须带服务端签名，缺签名会被拒绝，
			// 而且历史可能来自别的协议。本适配器不开启 extended thinking，丢弃思维链不影响正确性。
			if text := m.Text(); text != "" || len(m.ToolCalls) == 0 {
				blocks = append(blocks, textBlock{Type: "text", Text: text})
			}
			for _, tc := range m.ToolCalls {
				input := json.RawMessage(tc.Arguments)
				if len(input) == 0 {
					input = json.RawMessage("{}")
				}
				blocks = append(blocks, toolUseBlock{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: input})
			}
			out.Messages = append(out.Messages, message{Role: "assistant", Content: blocks})
		case agent.RoleTool:
			block := toolResultBlock{Type: "tool_result", ToolUseID: m.ToolID, Content: toolResultContent(m)}
			// tool_result 必须紧跟对应的 tool_use。历史被截断后可能只剩结果、没有配对调用，
			// 原样发会被服务端拒绝，这里改为普通文本，保住信息也不破坏协议约束。
			if !hasToolUse(out.Messages, m.ToolID) {
				if n := len(out.Messages); n > 0 && out.Messages[n-1].Role == "user" && isPlainUserText(out.Messages[n-1]) {
					out.Messages[n-1].Content = append(out.Messages[n-1].Content,
						textBlock{Type: "text", Text: "[工具结果 " + m.ToolID + "]\n" + m.Text()})
					continue
				}
				out.Messages = append(out.Messages, message{Role: "user", Content: []any{
					textBlock{Type: "text", Text: "[工具结果 " + m.ToolID + "]\n" + m.Text()},
				}})
				continue
			}
			// 上一条也是 tool 时并入同一条 user 消息。
			if n := len(out.Messages); n > 0 && out.Messages[n-1].Role == "user" && isToolResult(out.Messages[n-1]) {
				out.Messages[n-1].Content = append(out.Messages[n-1].Content, block)
				continue
			}
			out.Messages = append(out.Messages, message{Role: "user", Content: []any{block}})
		}
	}
	for _, t := range req.Tools {
		schema := json.RawMessage(t.Parameters)
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		out.Tools = append(out.Tools, tool{Name: t.Name, Description: t.Description, InputSchema: schema})
	}
	return out
}

// toolResultContent 构造 tool_result 的 content：纯文本发字符串，带图片时发块数组。
func toolResultContent(m agent.Message) any {
	if !m.HasImages() {
		return m.Text()
	}
	return contentBlocks(m)
}

// isToolResult 判断消息是否只含 tool_result block（由本包构造，类型可断言）。
func isToolResult(m message) bool {
	for _, b := range m.Content {
		if _, ok := b.(toolResultBlock); !ok {
			return false
		}
	}
	return len(m.Content) > 0
}

// isPlainUserText 判断消息是否只含 text block。
func isPlainUserText(m message) bool {
	for _, b := range m.Content {
		if _, ok := b.(textBlock); !ok {
			return false
		}
	}
	return len(m.Content) > 0
}

// hasToolUse 报告已构造的消息里是否存在指定 id 的 tool_use。
func hasToolUse(msgs []message, id string) bool {
	for _, m := range msgs {
		for _, b := range m.Content {
			if tu, ok := b.(toolUseBlock); ok && tu.ID == id {
				return true
			}
		}
	}
	return false
}

// streamEvent 是流式响应里的一个事件，各类型共用此结构，按 Type 取用对应字段。
type streamEvent struct {
	Type         string        `json:"type"`
	Index        int           `json:"index"`
	Message      *startMessage `json:"message"`
	ContentBlock contentBlock  `json:"content_block"`
	Delta        delta         `json:"delta"`
	Usage        *usage        `json:"usage"`
	Error        *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type startMessage struct {
	Usage *usage `json:"usage"`
}

type usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type contentBlock struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

type delta struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Thinking    string `json:"thinking"`
	PartialJSON string `json:"partial_json"`
	StopReason  string `json:"stop_reason"`
}
