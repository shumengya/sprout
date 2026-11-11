package agent

import (
	"encoding/json"

	"github.com/shumengya/sprout/internal/agent/event"
)

// Thinking 是一次请求的思考参数，已由上层按 models.jsonc 里当前档位的声明换算好。
// 内核不解释这些值，也不认识任何厂商字段名：各协议适配器把自己认识的部分写进请求体。
// 零值表示“不发送任何思考参数”，请求与没有这个功能时逐字节一致。
type Thinking struct {
	// Level 是档位名（如 high），只用于日志和展示，不参与请求构造。
	Level string
	// Effort 是思考强度枚举（OpenAI 的 reasoning_effort、Anthropic 的 output_config.effort、
	// Gemini 的 thinkingLevel）。空串表示不发。
	Effort string
	// Budget 是思考预算 token（Anthropic 的 budget_tokens、Gemini 的 thinkingBudget）。
	// nil 表示不发；0 是有意义的值：表示关闭思考。
	Budget *int
	// Summary 要求服务端返回可读的思考摘要（Anthropic display、Responses reasoning.summary、
	// Gemini includeThoughts）。只在 Effort 或 Budget 生效时随之发送。
	Summary bool
	// Extra 是合并进请求体顶层的额外字段，后写覆盖，用于厂商私有开关（如 DeepSeek 的 thinking.type）。
	Extra map[string]json.RawMessage
}

// Active 报告是否需要发送思考强度或预算。
func (t Thinking) Active() bool { return t.Effort != "" || t.Budget != nil }

// Native 是 provider 返回的原始 assistant 内容，定义在 event 包里，会话记录也要落盘它。
type Native = event.Native
