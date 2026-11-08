package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ToolSpec 是下发给模型的工具声明。
type ToolSpec struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// Request 是一次模型调用的完整输入。每次都带全量历史，provider 不保存服务端状态。
type Request struct {
	Model           string
	System          string
	Messages        []Message
	Tools           []ToolSpec
	MaxOutputTokens int
}

// Chunk 是流式输出的一个增量。tool call 不走 Chunk，流结束后随 Response 一起返回。
type Chunk struct {
	TextDelta      string
	ReasoningDelta string // 思维链增量，仅部分模型有
}

// Usage 是 token 用量，provider 没有返回时全为 0。
type Usage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

// 模型停止生成的原因，已由 provider 适配器归一化。
const (
	StopStop      = "stop"       // 正常结束
	StopToolCalls = "tool_calls" // 要求调用工具
	StopLength    = "length"     // 输出被长度上限截断
)

// Response 是一次模型调用的最终结果。
type Response struct {
	Message    Message
	Usage      Usage
	StopReason string
}

// Provider 抽象一个 LLM 渠道。Stream 通过 emit 推送文本增量，结束后返回完整 Response。
// ctx 被取消时应尽快返回，错误可以是 ctx.Err() 或包装了它的错误。
type Provider interface {
	Stream(ctx context.Context, req Request, emit func(Chunk) error) (Response, error)
}

// ProviderError 是 provider 适配器统一返回的错误，Error() 给出可直接显示给用户的中文说明。
type ProviderError struct {
	Status    int    // HTTP 状态码；0 表示没有拿到响应（网络层失败）
	Code      string // 服务端给出的错误码，如 context_length_exceeded
	Message   string // 服务端给出的原始错误信息
	Retryable bool
	Err       error // 底层错误（网络、解析等）
}

func (e *ProviderError) Unwrap() error { return e.Err }

func (e *ProviderError) Error() string {
	var hint string
	switch {
	case e.Status == 0:
		hint = "与模型服务通信失败，请检查网络和 baseUrl"
	case e.contextTooLong():
		hint = "上下文超出模型窗口，对话太长了，请重新开始一个会话"
	case e.Status == 401:
		hint = "认证失败（HTTP 401），API Key 无效或已过期，请检查 models.jsonc 或对应的环境变量"
	case e.Status == 403:
		hint = "没有权限（HTTP 403），当前账号无权访问该模型或接口"
	case e.Status == 404:
		hint = "接口或模型不存在（HTTP 404），请检查 baseUrl 和模型名"
	case e.Status == 429:
		hint = "请求过于频繁或额度不足（HTTP 429），请稍后重试"
	case e.Status >= 500:
		hint = fmt.Sprintf("模型服务出错（HTTP %d），可稍后重试", e.Status)
	default:
		hint = fmt.Sprintf("模型服务返回错误（HTTP %d）", e.Status)
	}
	detail := e.Message
	if detail == "" && e.Err != nil {
		detail = e.Err.Error()
	}
	if detail == "" {
		return hint
	}
	return hint + "：" + detail
}

// contextTooLong 识别各家对“输入超长”的不同表述。
func (e *ProviderError) contextTooLong() bool {
	if e.Status != 400 && e.Status != 413 {
		return false
	}
	if e.Code == "context_length_exceeded" {
		return true
	}
	msg := strings.ToLower(e.Message)
	for _, s := range []string{"context length", "context_length", "maximum context", "too many tokens", "prompt is too long"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
