// Package event 定义内核与各 surface（TUI / CLI / RPC）共用的事件协议。
//
// 本包不依赖任何其他内部包：surface 只通过 Event 观察内核，
// 会话落盘也是订阅同一条事件流，不另开通道。
package event

import (
	"encoding/json"
	"time"
)

// Kind 是事件的稳定标识，供序列化和日志使用。
type Kind string

const (
	KindTurnStarted      Kind = "turn.started"
	KindMessageDelta     Kind = "message.delta"
	KindReasoningDelta   Kind = "reasoning.delta"
	KindMessageCompleted Kind = "message.completed"
	KindToolRequested    Kind = "tool.requested"
	KindToolFinished     Kind = "tool.finished"
	KindUsage            Kind = "usage"
	KindRetrying         Kind = "retrying"
	KindCompacted        Kind = "compacted"
	KindTurnFinished     Kind = "turn.finished"
	KindInterrupted      Kind = "interrupted"
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
// Images 是本轮携带的图片；Data 是 base64 原始数据，会话记录器会把它落盘并清空，转发给 surface 的副本不含 Data。
type TurnStarted struct {
	Prompt string
	Images []ImageInfo
}

// ImageInfo 描述一张随输入提交的图片。Name 只是给用户看的文件名，不参与落盘路径；
// Path 由会话记录器在落盘后回填（相对会话文件的路径），未落盘的图片为空。
type ImageInfo struct {
	Name      string
	MediaType string
	Bytes     int
	Data      string // base64；记录器落盘后清空，避免事件流里重复携带大块内容
	Path      string
	// Source 是图片在消息文本里对应的原始片段，surface 用它把路径显示成 [文件名]。
	Source string
}

// MessageDelta 是 assistant 文本的流式增量。
type MessageDelta struct {
	Text string
}

// ReasoningDelta 是模型思维链的流式增量（DeepSeek 等思考模式模型的 reasoning_content）。
// 只用于展示；完整内容在 MessageCompleted.Reasoning 里。
type ReasoningDelta struct {
	Text string
}

// MessageCompleted 在一次模型调用结束后发出，携带完整的 assistant 消息。
// 会话记录只保存它，不保存 MessageDelta / ReasoningDelta。
type MessageCompleted struct {
	Content    string
	Reasoning  string
	ToolCalls  []ToolCall
	StopReason string
	// ReasoningTokensEst 是内核对 Reasoning 的 token 估算，供界面在服务端没报告思维链 token
	// （Usage.ReasoningTokens 为 0）时显示。由 Reasoning 算出，不落盘；会话恢复时重新计算。
	ReasoningTokensEst int
	// Native 是协议原始内容（如带签名的 Anthropic 思考块），会话记录原样落盘，恢复后同一模型继续回传。
	// 界面不需要它。
	Native *Native
}

// Native 是 provider 返回的原始 assistant 内容，供同一协议、同一模型在后续请求里原样回传。
//
// 有的协议要求思考块连同签名逐字节回传（Anthropic 开启思考后，带 tool_use 的 assistant 消息
// 必须带着原始 thinking block），内部消息模型表达不了这些细节，就整段保存下来。
// 内核只负责保存、落盘和在换模型时丢弃，不解析 Data。
type Native struct {
	Protocol string          `json:"protocol"` // 产生它的协议，如 anthropic-messages
	Model    string          `json:"model"`    // 产生它的模型；换了模型就不能再回传
	Data     json.RawMessage `json:"data"`     // 该协议的原始内容
}

// Usable 报告这段原始内容能否在 protocol/model 的请求里回传。
func (n *Native) Usable(protocol, model string) bool {
	return n != nil && len(n.Data) > 0 && n.Protocol == protocol && n.Model == model
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
	// Content 是回给模型的完整文本（含命令、退出码、stdout/stderr 的格式化结果，或失败说明）。
	// 会话记录保存它，恢复会话时原样还原，模型看到的就和当初一模一样。
	Content string
	// Err 非空表示工具没能产出正常结果（参数无效、进程起不来等）。
	Err string
}

// Usage 是一次模型调用的 token 用量。它紧跟在同一次调用的 MessageCompleted 之后发出。
type Usage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	// ReasoningTokens 是 OutputTokens 里用于思维链的部分；provider 不报告时为 0。
	ReasoningTokens int
}

// Retrying 在一次模型调用失败、即将退避重试前发出。只有在这次调用还没有向界面输出任何内容时才会重试，
// 所以不会出现半截答案被重复输出的情况。
type Retrying struct {
	Attempt int           // 第几次重试，从 1 开始
	Max     int           // 最多重试几次
	Delay   time.Duration // 等多久再试
	Reason  string        // 失败原因（首行）
}

// Compacted 在上下文被压缩后发出。压缩有两种：裁剪旧的工具输出（prune），
// 或把更早的对话总结成一段摘要（summary）。压缩从不静默发生，历史被改动就一定有这个事件。
type Compacted struct {
	Mode         string // CompactPrune 或 CompactSummary
	TokensBefore int    // 估算值
	TokensAfter  int    // 估算值
	Dropped      int    // summary：被摘要替代的消息条数；prune：被裁剪的工具输出条数
	Summary      string // summary：压缩后的完整摘要文本（会话记录保存它，恢复时用）
	Err          string // 非空表示摘要失败，历史未改动，本轮照常继续
}

// 压缩的种类。
const (
	CompactPrune   = "prune"
	CompactSummary = "summary"
)

// TurnFinished 在一轮结束时发出，每轮必有且仅有一次，是 surface 判断“这一轮结束了”的唯一依据。
type TurnFinished struct {
	Reason string
}

// Interrupted 在一轮被取消或中途失败、但已经收到部分输出时发出。
//
// 这轮不会作废：已经收到的正文连同中断原因会作为一条 assistant 消息留在历史里，
// 下一次请求模型就知道自己说到一半被打断了。会话记录保存它，恢复时还原成同样的消息。
// 它之后一定跟着 TurnFinished，Reason 为 cancelled 或 error。
type Interrupted struct {
	// Reason 是 event.ReasonCancelled 或 event.ReasonError。
	Reason string
	// Content 是写进历史的完整文本：已经收到的正文 + 一句中断说明。
	Content   string
	Reasoning string // 已经收到的思维链
	// Partial 为 true 表示中断前已经向界面输出过正文，界面不必再显示 Content。
	Partial bool
}

func (Interrupted) Kind() Kind { return KindInterrupted }

// Error 描述一次失败。它之后一定跟着 TurnFinished，Reason 为 ReasonError 或 ReasonMaxSteps。
type Error struct {
	Message   string
	Retryable bool
}

func (TurnStarted) Kind() Kind      { return KindTurnStarted }
func (MessageDelta) Kind() Kind     { return KindMessageDelta }
func (ReasoningDelta) Kind() Kind   { return KindReasoningDelta }
func (MessageCompleted) Kind() Kind { return KindMessageCompleted }
func (ToolRequested) Kind() Kind    { return KindToolRequested }
func (ToolFinished) Kind() Kind     { return KindToolFinished }
func (Usage) Kind() Kind            { return KindUsage }
func (Retrying) Kind() Kind         { return KindRetrying }
func (Compacted) Kind() Kind        { return KindCompacted }
func (TurnFinished) Kind() Kind     { return KindTurnFinished }
func (Error) Kind() Kind            { return KindError }
