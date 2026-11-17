package tui

import (
	"testing"

	"github.com/shumengya/sprout/internal/agent/event"
)

func sub(e event.Event) event.Subagent {
	return event.Subagent{CallID: "call_1", Agent: "explore", Event: e}
}

func TestSubagentProgressShowsOnAgentCard(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 40)
	begin(m, "查一下")
	apply(m,
		event.Usage{InputTokens: 500, OutputTokens: 10},
		event.ToolRequested{Call: event.ToolCall{ID: "call_1", Name: "agent", Arguments: `{"description":"查找重试逻辑","prompt":"p","agent":"explore"}`}},
		sub(event.TurnStarted{Prompt: "p"}),
		sub(event.ToolRequested{Call: event.ToolCall{ID: "s1", Name: "read", Arguments: `{"path":"internal/agent/retry.go"}`}}),
		sub(event.ToolFinished{ID: "s1", Name: "read"}),
		sub(event.ToolRequested{Call: event.ToolCall{ID: "s2", Name: "bash", Arguments: `{"command":"grep -rn Retry ."}`}}),
		sub(event.Usage{InputTokens: 9000, OutputTokens: 1200, CacheReadTokens: 4000}),
	)
	s := plain(m)
	mustContain(t, s, "agent(explore: 查找重试逻辑)", "子代理 explore · 2 次工具调用 · 输出 1.2k token",
		"✓ read(internal/agent/retry.go)", "▸ bash(grep -rn Retry .)")

	// 子代理的用量计入花费与总量，但上下文占用仍是主对话最近一次请求的输入。
	if m.usage.Output != 1210 || m.usage.CacheRead != 4000 || m.usage.Input != 500+5000 {
		t.Errorf("usage = %+v", m.usage)
	}
	if m.ctxTokens != 500 {
		t.Errorf("子代理的输入不该算进主对话的上下文占用: %d", m.ctxTokens)
	}

	apply(m,
		sub(event.ToolFinished{ID: "s2", Name: "bash", ExitCode: 1}),
		sub(event.TurnFinished{Reason: event.ReasonStop}),
		event.ToolFinished{ID: "call_1", Name: "agent", Command: "explore: 查找重试逻辑", Stdout: "结论：重试在 retry.go:20"},
	)
	s = plain(m)
	mustContain(t, s, "✗ bash(grep -rn Retry .)", "结论：重试在 retry.go:20")
	mustNotContain(t, s, "▸ ")
}

func TestSubagentCardCollapsesLongStepList(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 60)
	begin(m, "q")
	apply(m, event.ToolRequested{Call: event.ToolCall{ID: "call_1", Name: "agent", Arguments: `{"description":"d","prompt":"p"}`}})
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		apply(m, sub(event.ToolRequested{Call: event.ToolCall{ID: id, Name: "read", Arguments: `{"path":"` + id + `.go"}`}}),
			sub(event.ToolFinished{ID: id, Name: "read"}))
	}
	s := plain(m)
	mustContain(t, s, "… 前面还有 2 次", "read(c.go)", "read(f.go)")
	mustNotContain(t, s, "read(a.go)", "read(b.go)")
}

// 恢复会话时子代理用量排在工具卡片之前到达：只计花费，不能因为找不到卡片而出错。
func TestSubagentUsageWithoutCardOnlyCounts(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 30)
	apply(m, sub(event.Usage{InputTokens: 100, OutputTokens: 20}))
	if m.usage.Output != 20 || m.ctxTokens != 0 {
		t.Errorf("usage=%+v ctx=%d", m.usage, m.ctxTokens)
	}
}
