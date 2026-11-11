package anthropic

import (
	"encoding/json"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/config"
)

// Messages 协议的线格式。只含本项目用到的字段。

type request struct {
	Model     string    `json:"model"`
	System    string    `json:"system,omitempty"`
	Messages  []message `json:"messages"`
	Tools     []tool    `json:"tools,omitempty"`
	Stream    bool      `json:"stream"`
	MaxTokens int       `json:"max_tokens"`
	// Thinking 是 adaptiveThinking 或 budgetThinking，只在 models.jsonc 为当前档位声明了取值时才发。
	Thinking     any           `json:"thinking,omitempty"`
	OutputConfig *outputConfig `json:"output_config,omitempty"`
}

// adaptiveThinking 由模型自己决定思考多少，深度用 output_config.effort 控制（Claude 4.6 起的推荐写法；
// 较新的模型已经不接受 budget_tokens）。display 不写时新模型返回空的思考文本，要展示就得显式要摘要。
type adaptiveThinking struct {
	Type    string `json:"type"` // "adaptive"
	Display string `json:"display,omitempty"`
}

// budgetThinking 是固定预算的写法，只给不支持 adaptive 的老模型用；type 为 disabled 时没有预算。
type budgetThinking struct {
	Type         string `json:"type"` // "enabled" | "disabled"
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

type outputConfig struct {
	Effort string `json:"effort,omitempty"`
}

// answerReserve 是预算模式下给最终回答留的 token：budget_tokens 必须小于 max_tokens，
// 没配 maxOutput 时默认上限可能比预算还小，就抬到“预算 + 这么多”。
const answerReserve = 4096

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
	applyThinking(&out, req.Thinking)
	// 已发出的 tool_use id。原始内容回传的 assistant 消息里的 tool_use 也要登记，否则后面的结果会被当成孤儿。
	toolUses := map[string]bool{}
	for _, m := range req.Messages {
		switch m.Role {
		case agent.RoleUser:
			out.Messages = append(out.Messages, message{Role: "user", Content: contentBlocks(m)})
		case agent.RoleAssistant:
			// 同一模型产生的、带思考块的消息原样回传：思考块的签名只对原始内容有效，
			// 开启思考时带 tool_use 的 assistant 消息缺了它会被服务端拒绝。
			if blocks, ok := nativeBlocks(m, req.Model, toolUses); ok {
				out.Messages = append(out.Messages, message{Role: "assistant", Content: blocks})
				continue
			}
			var blocks []any
			// 没有原始内容时不回传 Reasoning：思考块必须带服务端签名，单凭文本伪造会被拒绝，
			// 而且历史可能来自别的协议。服务端允许更早的轮次不带思考块。
			if text := m.Text(); text != "" || len(m.ToolCalls) == 0 {
				blocks = append(blocks, textBlock{Type: "text", Text: text})
			}
			for _, tc := range m.ToolCalls {
				input := json.RawMessage(tc.Arguments)
				if len(input) == 0 {
					input = json.RawMessage("{}")
				}
				blocks = append(blocks, toolUseBlock{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: input})
				toolUses[tc.ID] = true
			}
			out.Messages = append(out.Messages, message{Role: "assistant", Content: blocks})
		case agent.RoleTool:
			block := toolResultBlock{Type: "tool_result", ToolUseID: m.ToolID, Content: toolResultContent(m)}
			// tool_result 必须紧跟对应的 tool_use。历史被截断后可能只剩结果、没有配对调用，
			// 原样发会被服务端拒绝，这里改为普通文本，保住信息也不破坏协议约束。
			if !toolUses[m.ToolID] {
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

// applyThinking 把思考参数写进请求。强度走 adaptive + output_config.effort；预算走 enabled + budget_tokens，
// 预算为 0 表示显式关闭。两者都没有时什么也不发，请求与未开启时完全一致。
func applyThinking(out *request, t agent.Thinking) {
	switch {
	case t.Budget != nil && *t.Budget > 0:
		b := *t.Budget
		if out.MaxTokens <= b {
			out.MaxTokens = b + answerReserve
		}
		out.Thinking = budgetThinking{Type: "enabled", BudgetTokens: b}
	case t.Budget != nil:
		out.Thinking = budgetThinking{Type: "disabled"}
	case t.Effort != "":
		th := adaptiveThinking{Type: "adaptive"}
		if t.Summary {
			th.Display = "summarized"
		}
		out.Thinking = th
	}
	if t.Effort != "" {
		out.OutputConfig = &outputConfig{Effort: t.Effort}
	}
}

// nativeBlocks 取出可以原样回传的原始内容块，并登记其中的 tool_use id。
// 原始内容不属于当前模型、或者解析不了时返回 false，调用方按内部消息重建。
func nativeBlocks(m agent.Message, model string, toolUses map[string]bool) ([]any, bool) {
	if !m.Native.Usable(config.ProtocolAnthropic, model) {
		return nil, false
	}
	var raw []json.RawMessage
	if json.Unmarshal(m.Native.Data, &raw) != nil || len(raw) == 0 {
		return nil, false
	}
	blocks := make([]any, len(raw))
	for i, b := range raw {
		var head struct{ Type, ID string }
		if json.Unmarshal(b, &head) != nil {
			return nil, false
		}
		if head.Type == "tool_use" {
			toolUses[head.ID] = true
		}
		blocks[i] = b
	}
	return blocks, true
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
	// Data 是 redacted_thinking 的加密内容，在 content_block_start 里一次给全。
	Data string `json:"data"`
}

type delta struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Thinking    string `json:"thinking"`
	Signature   string `json:"signature"`
	PartialJSON string `json:"partial_json"`
	StopReason  string `json:"stop_reason"`
}

// 回传用的原始块。字段顺序与服务端一致，签名对内容本身生效，与 JSON 键顺序无关。
type thinkingBlock struct {
	Type      string `json:"type"` // "thinking"
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
}

type redactedBlock struct {
	Type string `json:"type"` // "redacted_thinking"
	Data string `json:"data"`
}
