package session

import (
	"time"

	"github.com/shumengya/sprout/internal/agent/event"
)

// JSONL 里每行一条记录，type 字段区分种类。只记录消息级内容，不记录 token 增量。

type header struct {
	Type string    `json:"type"`
	Time time.Time `json:"time"`
}

type sessionRec struct {
	header
	ID       string   `json:"id"`
	Cwd      string   `json:"cwd"`
	Provider string   `json:"provider"`
	Model    string   `json:"model"`
	Tools    []string `json:"tools"`
	Version  string   `json:"version,omitempty"`
}

type userRec struct {
	header
	Content string `json:"content"`
}

type toolCallRec struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type assistantRec struct {
	header
	Content    string        `json:"content"`
	Reasoning  string        `json:"reasoning,omitempty"`
	ToolCalls  []toolCallRec `json:"tool_calls,omitempty"`
	StopReason string        `json:"stop_reason,omitempty"`
}

// toolRec 的 exit_code 不能 omitempty：0 是有意义的值。
type toolRec struct {
	header
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	Command    string `json:"command,omitempty"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
	ExitCode   int    `json:"exit_code"`
	Truncated  bool   `json:"truncated,omitempty"`
	TimedOut   bool   `json:"timed_out,omitempty"`
	Cancelled  bool   `json:"cancelled,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

type usageRec struct {
	header
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type turnRec struct {
	header
	Reason string `json:"reason"`
}

type errorRec struct {
	header
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
}

// toRecord 把内核事件转成会话记录。不需要落盘的事件返回 false：
// MessageDelta 只是增量（完整内容在 MessageCompleted 里），ToolRequested 的信息已含在 assistant 记录的 tool_calls 里。
func toRecord(e event.Event, now time.Time) (any, bool) {
	h := func(t string) header { return header{Type: t, Time: now} }
	switch e := e.(type) {
	case event.TurnStarted:
		return userRec{h("user"), e.Prompt}, true
	case event.MessageCompleted:
		rec := assistantRec{header: h("assistant"), Content: e.Content, Reasoning: e.Reasoning, StopReason: e.StopReason}
		for _, c := range e.ToolCalls {
			rec.ToolCalls = append(rec.ToolCalls, toolCallRec{c.ID, c.Name, c.Arguments})
		}
		return rec, true
	case event.ToolFinished:
		return toolRec{
			header: h("tool"), ToolCallID: e.ID, Name: e.Name, Command: e.Command,
			Stdout: e.Stdout, Stderr: e.Stderr, ExitCode: e.ExitCode,
			Truncated: e.Truncated, TimedOut: e.TimedOut, Cancelled: e.Cancelled,
			DurationMS: e.Duration.Milliseconds(), Error: e.Err,
		}, true
	case event.Usage:
		return usageRec{h("usage"), e.InputTokens, e.OutputTokens, e.TotalTokens}, true
	case event.TurnFinished:
		return turnRec{h("turn"), e.Reason}, true
	case event.Error:
		return errorRec{h("error"), e.Message, e.Retryable}, true
	}
	return nil, false
}
