package rpc

import (
	"github.com/shumengya/sprout/internal/agent/event"
)

// 内核的事件类型没有 json 标签，字段名也是 Go 风格；这里定义线上格式，字段一律 camelCase，
// 时长换成毫秒。改字段时同步 docs/rpc.md：这是客户端依赖的接口。

type wireImage struct {
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
	Bytes     int    `json:"bytes"`
	Path      string `json:"path,omitempty"`
	Source    string `json:"source,omitempty"`
}

// wireImages 总是返回非 nil 切片：客户端拿到的是 []，不用区分 null。
func wireImages(in []event.ImageInfo) []wireImage {
	out := make([]wireImage, 0, len(in))
	for _, im := range in {
		out = append(out, wireImage{Name: im.Name, MediaType: im.MediaType, Bytes: im.Bytes, Path: im.Path, Source: im.Source})
	}
	return out
}

type wireToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// wireEvent 把一个内核事件转成 event 通知的 params：公共的 turn、kind 加上该事件的字段。
// 不认识的事件返回 nil（不推送），内核加了新事件而这里没跟上时不会发出半截的消息。
func wireEvent(turn int, e event.Event) map[string]any {
	m := map[string]any{"turn": turn, "kind": string(e.Kind())}
	switch e := e.(type) {
	case event.TurnStarted:
		m["prompt"] = e.Prompt
		// 不带 base64：客户端自己有原图，事件里重复携带只会让单行变成几 MB。
		m["images"] = wireImages(e.Images)
	case event.MessageDelta:
		m["text"] = e.Text
	case event.ReasoningDelta:
		m["text"] = e.Text
	case event.MessageCompleted:
		calls := make([]wireToolCall, 0, len(e.ToolCalls))
		for _, c := range e.ToolCalls {
			calls = append(calls, wireToolCall(c))
		}
		m["content"] = e.Content
		m["reasoning"] = e.Reasoning
		m["toolCalls"] = calls
		m["stopReason"] = e.StopReason
		m["reasoningTokensEst"] = e.ReasoningTokensEst
	case event.ToolRequested:
		m["id"], m["name"], m["arguments"] = e.Call.ID, e.Call.Name, e.Call.Arguments
	case event.ToolFinished:
		m["id"], m["name"], m["command"] = e.ID, e.Name, e.Command
		m["stdout"], m["stderr"], m["exitCode"] = e.Stdout, e.Stderr, e.ExitCode
		m["truncated"], m["timedOut"], m["cancelled"] = e.Truncated, e.TimedOut, e.Cancelled
		m["durationMs"] = e.Duration.Milliseconds()
		m["content"], m["error"] = e.Content, e.Err
		// 工具结果里的图片同样只给元数据；要看原图从会话目录的 <会话名>.images/ 读。
		m["images"] = wireImages(e.Images)
	case event.Usage:
		m["inputTokens"], m["outputTokens"], m["totalTokens"] = e.InputTokens, e.OutputTokens, e.TotalTokens
		m["reasoningTokens"] = e.ReasoningTokens
		m["cacheReadTokens"], m["cacheWriteTokens"] = e.CacheReadTokens, e.CacheWriteTokens
	case event.Retrying:
		m["attempt"], m["max"], m["delayMs"], m["reason"] = e.Attempt, e.Max, e.Delay.Milliseconds(), e.Reason
	case event.Compacted:
		m["mode"], m["tokensBefore"], m["tokensAfter"] = e.Mode, e.TokensBefore, e.TokensAfter
		m["dropped"], m["summary"], m["error"], m["manual"] = e.Dropped, e.Summary, e.Err, e.Manual
	case event.Interrupted:
		m["reason"], m["content"], m["reasoning"], m["partial"] = e.Reason, e.Content, e.Reasoning, e.Partial
	case event.Error:
		m["message"], m["retryable"] = e.Message, e.Retryable
	case event.TurnFinished:
		m["reason"] = e.Reason
	default:
		return nil
	}
	return m
}
