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

// imageRec 是用户输入里的一张图片。文件放在会话目录下的 <会话名>.images/ 里，
// File 存相对会话目录的路径，会话整体搬走也能恢复；不把 base64 写进 JSONL，避免单行过大。
type imageRec struct {
	File      string `json:"file"`
	MediaType string `json:"media_type"`
	Bytes     int    `json:"bytes"`
}

type userRec struct {
	header
	Content string     `json:"content"`
	Images  []imageRec `json:"images,omitempty"`
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
	// Content 是回给模型的完整文本。恢复会话时原样还原，模型看到的就和当初一模一样。
	Content string `json:"content,omitempty"`
}

// compactRec 记录一次上下文压缩。恢复会话时按它重放：summary 丢弃前 dropped 条消息并换上摘要，
// prune 重新执行同样的裁剪（PruneToolOutputs 是纯函数，结果和当初一致）。
type compactRec struct {
	header
	Mode         string `json:"mode"`
	TokensBefore int    `json:"tokens_before"`
	TokensAfter  int    `json:"tokens_after"`
	Dropped      int    `json:"dropped"`
	Summary      string `json:"summary,omitempty"`
	Error        string `json:"error,omitempty"`
}

type usageRec struct {
	header
	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	TotalTokens     int `json:"total_tokens"`
	ReasoningTokens int `json:"reasoning_tokens,omitempty"` // 服务端不报告时省略
}

type turnRec struct {
	header
	Reason string `json:"reason"`
}

// resumeRec 标记会话在这里被恢复续接，并记录续接时用的模型。恢复重放时忽略，仅供审计。
type resumeRec struct {
	header
	Provider string   `json:"provider"`
	Model    string   `json:"model"`
	Tools    []string `json:"tools"`
}

type errorRec struct {
	header
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
}

// retryRec 记录一次退避重试，便于事后审计网络或服务端问题；恢复会话时忽略。
type retryRec struct {
	header
	Attempt int    `json:"attempt"`
	Max     int    `json:"max"`
	DelayMS int64  `json:"delay_ms"`
	Reason  string `json:"reason"`
}

// toRecord 把内核事件转成会话记录。不需要落盘的事件返回 false：
// MessageDelta 只是增量（完整内容在 MessageCompleted 里），ToolRequested 的信息已含在 assistant 记录的 tool_calls 里。
func toRecord(e event.Event, now time.Time) (any, bool) {
	h := func(t string) header { return header{Type: t, Time: now} }
	switch e := e.(type) {
	case event.TurnStarted:
		return userRec{h("user"), e.Prompt, imageRecs(e.Images)}, true
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
			DurationMS: e.Duration.Milliseconds(), Error: e.Err, Content: e.Content,
		}, true
	case event.Compacted:
		return compactRec{h("compact"), e.Mode, e.TokensBefore, e.TokensAfter, e.Dropped, e.Summary, e.Err}, true
	case event.Usage:
		return usageRec{h("usage"), e.InputTokens, e.OutputTokens, e.TotalTokens, e.ReasoningTokens}, true
	case event.TurnFinished:
		return turnRec{h("turn"), e.Reason}, true
	case event.Error:
		return errorRec{h("error"), e.Message, e.Retryable}, true
	case event.Retrying:
		return retryRec{h("retry"), e.Attempt, e.Max, e.Delay.Milliseconds(), e.Reason}, true
	}
	return nil, false
}

// imageRecs 把事件里的图片描述转成记录里的图片引用。文件路径由记录器在落盘图片后回填，
// 这里只保留名称、类型和大小；没有落盘的图片（写文件失败）不该出现在记录里。
func imageRecs(infos []event.ImageInfo) []imageRec {
	if len(infos) == 0 {
		return nil
	}
	out := make([]imageRec, 0, len(infos))
	for _, in := range infos {
		if in.Path == "" { // 没落盘的图片不进记录，否则重放时会多出一条“无法恢复”的说明
			continue
		}
		out = append(out, imageRec{File: in.Path, MediaType: in.MediaType, Bytes: in.Bytes})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
