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
}

// loop 是 ReAct 主循环：调用模型 → 有 tool call 就执行并把结果回给模型 → 直到没有 tool call。
func (t *turn) loop() error {
	a := t.a
	for step := 0; step < a.cfg.MaxSteps; step++ {
		if t.ctx.Err() != nil {
			return t.cancelled()
		}

		resp, err := a.cfg.Provider.Stream(t.ctx, a.request(), func(c Chunk) error {
			if c.TextDelta != "" {
				t.out.Emit(event.MessageDelta{Text: c.TextDelta})
			}
			return t.ctx.Err()
		})
		if err != nil {
			if t.ctx.Err() != nil {
				return t.cancelled()
			}
			return t.fail(err)
		}

		msg := resp.Message
		msg.Role = RoleAssistant
		msg.ToolCalls = normalizeCalls(msg.ToolCalls)
		a.history = append(a.history, msg)

		t.out.Emit(event.MessageCompleted{Content: msg.Content, ToolCalls: eventCalls(msg.ToolCalls), StopReason: resp.StopReason})
		if resp.Usage != (Usage{}) {
			t.out.Emit(event.Usage{
				InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens, TotalTokens: resp.Usage.TotalTokens,
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
	t.rollbackUnanswered()
	t.reason = event.ReasonCancelled
	return t.ctx.Err()
}

// fail 收尾一次失败的轮次：发 Error 事件并返回原错误。
func (t *turn) fail(err error) error {
	t.rollbackUnanswered()
	t.reason = event.ReasonError
	var pe *ProviderError
	t.out.Emit(event.Error{Message: err.Error(), Retryable: errors.As(err, &pe) && pe.Retryable})
	t.a.log.Warn("turn failed", "error", err.Error())
	return err
}

// rollbackUnanswered 在第一次模型调用就失败（或被取消）时撤销本轮的用户消息，
// 避免用户重试时历史里出现两条连续的相同提问。后续步骤失败时历史以 tool 结果结尾，
// 仍然是合法序列，不需要动。
func (t *turn) rollbackUnanswered() {
	if len(t.a.history) == t.base+1 {
		t.a.history = t.a.history[:t.base]
	}
}
