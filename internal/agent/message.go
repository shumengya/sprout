// Package agent 是 ReAct 内核：维护对话历史，反复调用 Provider 与工具直到模型给出最终答案。
//
// 内核只处理下面这套与协议无关的内部消息模型，不出现任何厂商字段；
// 协议转换由 internal/provider 下的适配器负责。内核不得 import 任何 surface 包。
package agent

// Role 是消息角色。
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall 是模型发起的一次工具调用。Arguments 是模型给出的原始 JSON 字符串，
// 不在内核里解析，由具体工具负责校验。
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// Message 是对话历史里的一条消息。
//
//   - assistant 消息可带 ToolCalls。
//   - tool 消息通过 ToolID 对应某个 ToolCall.ID，Content 是回给模型的结果文本。
type Message struct {
	Role      Role
	Content   string
	ToolCalls []ToolCall
	ToolID    string
}
