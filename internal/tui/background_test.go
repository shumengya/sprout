package tui

import (
	"testing"

	"github.com/shumengya/sprout/internal/agent/event"
)

// 转入后台的 agent 调用：卡片先显示“后台运行中”，子代理进度照常挂上去；模型给完答案后状态写“等待后台任务”；
// 结果送达后卡片换成报告，对话区末尾补一行说明。
func TestBackgroundAgentCard(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 40)
	begin(m, "查一下")
	apply(m,
		event.ToolRequested{Call: event.ToolCall{ID: "call_1", Name: "agent",
			Arguments: `{"description":"查找重试逻辑","prompt":"p","agent":"explore","run_in_background":true}`}},
		event.ToolFinished{ID: "call_1", Name: "agent", Content: "已转入后台执行", Background: true},
		sub(event.ToolRequested{Call: event.ToolCall{ID: "s1", Name: "read", Arguments: `{"path":"retry.go"}`}}),
		event.MessageCompleted{Content: "子代理在查，我等它的报告。"},
	)
	s := plain(m)
	mustContain(t, s, "agent(explore: 查找重试逻辑)", "后台运行中…", "▸ read(retry.go)")
	mustNotContain(t, s, "已转入后台执行") // 占位说明是给模型看的，卡片上不显示
	if got := m.busyLabel(); got != "等待 1 个后台任务" {
		t.Errorf("busyLabel = %q", got)
	}

	apply(m,
		sub(event.ToolFinished{ID: "s1", Name: "read"}),
		event.BackgroundFinished{Result: event.ToolFinished{ID: "call_1", Name: "agent", Command: "explore: 查找重试逻辑",
			Stdout: "结论：重试在 retry.go:20"}, Message: "[后台任务已结束：agent，任务编号 call_1]"},
	)
	s = plain(m)
	mustContain(t, s, "结论：重试在 retry.go:20", "后台任务已结束：agent(explore: 查找重试逻辑) · 1 行，exit 0")
	mustNotContain(t, s, "后台运行中…")
	if got := m.busyLabel(); got != "生成中" {
		t.Errorf("结果送达后内核接着请求模型，busyLabel = %q", got)
	}
}

// 后台任务在跑、模型同时在生成：状态写明后台还有几个。
func TestBusyLabelMentionsBackgroundWhileGenerating(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 40)
	begin(m, "q")
	apply(m,
		event.ToolRequested{Call: event.ToolCall{ID: "c1", Name: "agent", Arguments: `{"description":"d","prompt":"p"}`}},
		event.ToolFinished{ID: "c1", Name: "agent", Background: true},
		event.MessageDelta{Text: "接着"},
	)
	if got := m.busyLabel(); got != "生成中 · 后台 1 个" {
		t.Errorf("busyLabel = %q", got)
	}
	// 一轮异常结束时卡片不能一直停在“后台运行中”。
	apply(m, event.TurnFinished{Reason: event.ReasonCancelled})
	mustContain(t, plain(m), "已取消")
	mustNotContain(t, plain(m), "后台运行中…")
}
