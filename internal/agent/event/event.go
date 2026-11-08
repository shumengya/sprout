// Package event 定义内核与各 surface（TUI / CLI / RPC）共用的事件协议。
//
// 本包不依赖任何其他内部包：surface 只通过 Event 观察内核，
// 会话落盘也是订阅同一条事件流，不另开通道。
package event

import "time"

// Kind 是事件的稳定标识，供序列化和日志使用。
type Kind string

const (
	KindTurnStarted      Kind = "turn.started"
	KindMessageDelta     Kind = "message.delta"
	KindMessageCompleted Kind = "message.completed"
	KindToolRequested    Kind = "tool.requested"
	KindToolFinished     Kind = "tool.finished"
	KindUsage            Kind = "usage"
	KindTurnFinished     Kind = "turn.finished"
	KindError            Kind = "error"
)

// 一轮（turn）结束的原因。
const (
	ReasonStop      = "stop"      // 模型给出最终答案
	ReasonCancelled = "cancelled" // 用户取消
	ReasonError     = "error"     // 出错，前面一定有一条 Error 事件
	ReasonMaxSteps  = "max_steps" // 超过最大工具调用轮数
	ReasonLength    = "length"    // 模型输出被长度上限截断
)

// Event 是内核发出的所有事件的公共接口。
type Event interface {
	Kind() Kind
}

// ToolCall 是模型请求的一次工具调用。Arguments 是原始 JSON 字符串。
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// TurnStarted 在用户提交一条输入后发出，每轮必有且仅有一次。
type TurnStarted struct {
	Prompt string
}

// MessageDelta 是 assistant 文本的流式增量。
type MessageDelta struct {
	Text string
}

// MessageCompleted 在一次模型调用结束后发出，携带完整的 assistant 消息。
// 会话记录只保存它，不保存 MessageDelta。
type MessageCompleted struct {
	Content    string
	ToolCalls  []ToolCall
	StopReason string
}

// ToolRequested 在工具开始执行前发出。
type ToolRequested struct {
	Call ToolCall
}

// ToolFinished 在工具执行结束后发出，Stdout/Stderr 是未经预览截断的完整内容
// （但已受工具自身 50KB 上限约束）。
type ToolFinished struct {
	ID        string
	Name      string
	Command   string
	Stdout    string
	Stderr    string
	ExitCode  int
	Truncated bool
	TimedOut  bool
	Cancelled bool
	Duration  time.Duration
	// Err 非空表示工具没能产出正常结果（参数无效、进程起不来等）。
	Err string
}

// Usage 是一次模型调用的 token 用量。
type Usage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

// TurnFinished 在一轮结束时发出，每轮必有且仅有一次，是 surface 判断“这一轮结束了”的唯一依据。
type TurnFinished struct {
	Reason string
}

// Error 描述一次失败。它之后一定跟着 Reason 为 ReasonError 的 TurnFinished。
type Error struct {
	Message   string
	Retryable bool
}

func (TurnStarted) Kind() Kind      { return KindTurnStarted }
func (MessageDelta) Kind() Kind     { return KindMessageDelta }
func (MessageCompleted) Kind() Kind { return KindMessageCompleted }
func (ToolRequested) Kind() Kind    { return KindToolRequested }
func (ToolFinished) Kind() Kind     { return KindToolFinished }
func (Usage) Kind() Kind            { return KindUsage }
func (TurnFinished) Kind() Kind     { return KindTurnFinished }
func (Error) Kind() Kind            { return KindError }
