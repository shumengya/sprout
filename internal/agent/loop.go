package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/shumengya/sprout/internal/agent/event"
)

// turn 是一次 Run 的运行状态。
type turn struct {
	a      *Agent
	ctx    context.Context
	out    event.Sink
	base   int // 本轮开始前的历史长度，不含本轮的用户消息
	reason string
	// partial 是最近一次模型调用里已经流式收到的文本与思维链。
	// 调用被取消或中途失败时，它连同中断原因一起补进历史：中断的那段话仍是用户看到过的内容，
	// 下一次请求要让模型知道“我说过这些，然后被打断了”，否则用户说“继续”时模型会从零开始。
	partial string
	think   string
	// compactFailed：本轮已经尝试过摘要且失败，不再每一步都重试（否则每次模型调用前都多一次失败的摘要请求）。
	compactFailed bool
}

// stream 向模型发一次请求并把增量转成事件。
func (t *turn) stream() (Response, error) {
	t.partial, t.think = "", ""
	return t.retrying(func(started func()) (Response, error) {
		return t.a.cfg.Provider.Stream(t.ctx, t.a.request(), func(c Chunk) error {
			if c.ReasoningDelta != "" {
				started()
				t.think += c.ReasoningDelta
				t.out.Emit(event.ReasoningDelta{Text: c.ReasoningDelta})
			}
			if c.TextDelta != "" {
				started()
				t.partial += c.TextDelta
				t.out.Emit(event.MessageDelta{Text: c.TextDelta})
			}
			return t.ctx.Err()
		})
	})
}

// loop 是 ReAct 主循环：调用模型 → 有 tool call 就执行并把结果回给模型 → 直到没有 tool call。
// 默认不限轮数：真实任务里的"改代码 → 跑测试 → 修错"本来就可能有几十次工具调用，
// 硬上限会在任务做到一半时把一轮掐断。需要停止时由用户取消（Ctrl+C），
// 实现层面的失控由重试次数、失败两次换思路这些约束兜住。Config.MaxSteps > 0 时才限制。
func (t *turn) loop() error {
	a := t.a
	reactive := false // 本步是否已经因“服务端报告超长”压缩过一次
	for step := 0; a.cfg.MaxSteps <= 0 || step < a.cfg.MaxSteps; step++ {
		if t.ctx.Err() != nil {
			return t.cancelled()
		}
		if !reactive && !t.compactFailed {
			t.compact(false) // 主动压缩：历史接近窗口时先压缩再发
		}

		resp, err := t.stream()
		if err != nil {
			if t.ctx.Err() != nil {
				return t.cancelled()
			}
			// 服务端说上下文超长（我们的估算偏低，或窗口没配置）：压缩一次再试，这次不占步数。
			var pe *ProviderError
			if !reactive && errors.As(err, &pe) && pe.ContextTooLong() && t.compact(true) {
				reactive = true
				step--
				continue
			}
			return t.fail(err)
		}
		reactive = false

		// 用服务端报告的真实输入 token 校准估算；这一刻历史还不含本次的 assistant 消息。
		if resp.Usage.InputTokens > 0 {
			a.calibTokens, a.calibLen = resp.Usage.InputTokens, len(a.history)
		} else {
			a.resetCalibration()
		}

		msg := resp.Message
		msg.Role = RoleAssistant
		msg.Content = assistantMessage(msg.Text()).Content
		msg.ToolCalls = normalizeCalls(msg.ToolCalls)
		a.history = append(a.history, msg)
		// 这一段的正文已经完整进历史（含 tool call），之后的取消不该再补一次半截内容。
		t.partial, t.think = "", ""

		t.out.Emit(event.MessageCompleted{
			Content: msg.Text(), Reasoning: msg.Reasoning,
			ToolCalls: eventCalls(msg.ToolCalls), StopReason: resp.StopReason,
			ReasoningTokensEst: EstimateTokens(msg.Reasoning),
			Native:             msg.Native,
		})
		if resp.Usage != (Usage{}) {
			t.out.Emit(event.Usage{
				InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens, TotalTokens: resp.Usage.TotalTokens,
				ReasoningTokens: resp.Usage.ReasoningTokens,
			})
		}

		if len(msg.ToolCalls) == 0 {
			if resp.StopReason == StopLength {
				t.reason = event.ReasonLength
			}
			return nil
		}
		if err := t.runTools(msg.ToolCalls); err != nil {
			return err
		}
	}

	t.reason = event.ReasonMaxSteps
	err := fmt.Errorf("%w（%d 轮），已停止本轮", ErrMaxSteps, a.cfg.MaxSteps)
	t.out.Emit(event.Error{Message: err.Error()})
	return err
}

// normalizeCalls 把空参数补成 "{}"：部分模型对无参工具会给空串，
// 原样回传给协议会因为不是合法 JSON 而被服务端拒绝。
func normalizeCalls(calls []ToolCall) []ToolCall {
	for i := range calls {
		if strings.TrimSpace(calls[i].Arguments) == "" {
			calls[i].Arguments = "{}"
		}
	}
	return calls
}

func eventCalls(calls []ToolCall) []event.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]event.ToolCall, len(calls))
	for i, c := range calls {
		out[i] = event.ToolCall{ID: c.ID, Name: c.Name, Arguments: c.Arguments}
	}
	return out
}

// cancelled 收尾一次被取消的轮次。
func (t *turn) cancelled() error {
	t.reason = event.ReasonCancelled
	t.saveInterrupted(interruptNoteCancelled)
	return t.ctx.Err()
}

// fail 收尾一次失败的轮次：发 Error 事件并返回原错误。
func (t *turn) fail(err error) error {
	t.reason = event.ReasonError
	t.saveInterrupted(interruptNoteError + clip(err.Error(), maxInterruptReason) + "]")
	var pe *ProviderError
	t.out.Emit(event.Error{Message: err.Error(), Retryable: errors.As(err, &pe) && pe.Retryable})
	t.a.log.Warn("turn failed", "error", err.Error())
	return err
}

// 中断说明：写成方括号形式，在任何协议里都只是普通文本，模型能认出这是系统补的说明。
const (
	interruptNoteCancelled = "[对话中断：用户取消]"
	interruptNoteError     = "[对话中断："
	// maxInterruptReason 限制写进历史的错误原文长度：有的服务端错误会带上整段请求体。
	maxInterruptReason = 300
)

// saveInterrupted 把这一轮保留在历史里，并说明它为什么停下。
//
// 用户已经看到这轮提问和答案，历史里就该有；否则用户说“继续”时模型既不知道上一条问的是什么，
// 也不知道自己说到一半被打断了。即使一个字都没收到也写，中断本身也是这轮的一部分。
//
// 只保存文本与思维链：被取消时 tool call 的参数可能只收到一半，不是合法 JSON，
// 带进历史会让后续请求被服务端拒绝，所以半截调用整个丢弃。
// 半截内容和说明合成同一条 assistant 消息，历史里不会出现两条连续的 user 消息。
func (t *turn) saveInterrupted(note string) {
	if len(t.a.history) <= t.base { // 本轮没有用户消息可保留（不该发生）
		return
	}
	text := strings.TrimRight(t.partial, "\n")
	if text != "" {
		text += "\n\n"
	}
	text += note

	msg := assistantMessage(text)
	msg.Reasoning = t.think
	t.a.history = append(t.a.history, msg)
	t.a.resetCalibration()
	// 单独发 Interrupted，不让界面把它当成一次正常回复重复显示；
	// 会话记录按 interrupted 落盘，恢复时还原成同样的 assistant 消息。
	t.out.Emit(event.Interrupted{
		Reason: t.reason, Content: text, Reasoning: t.think, Partial: t.partial != "",
	})
	t.partial, t.think = "", ""
}
