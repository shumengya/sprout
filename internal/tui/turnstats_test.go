package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/shumengya/sprout/internal/agent/event"
)

// statsLines 返回对话区里的统计行，按出现顺序。
func statsLines(m *Model) []string {
	var out []string
	for _, e := range m.entries {
		if e.kind == kindStats {
			out = append(out, e.text)
		}
	}
	return out
}

func TestTurnStatsLineSumsAllCallsOfTheTurn(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 200, 40)
	clock := m.now()
	m.now = func() time.Time { return clock }
	m.started, m.stats = clock, turnStats{start: clock}

	// 第一次调用：1 秒后出首字，生成 2 秒。
	clock = clock.Add(time.Second)
	apply(m, event.MessageDelta{Text: "先看看"})
	clock = clock.Add(2 * time.Second)
	apply(m, event.MessageCompleted{Content: "先看看"})
	apply(m, event.Usage{InputTokens: 1000, OutputTokens: 100, CacheReadTokens: 800})
	// 工具跑 5 秒，不算进速度，但算进耗时。
	apply(m, event.ToolRequested{Call: event.ToolCall{ID: "t1", Name: "bash"}})
	clock = clock.Add(5 * time.Second)
	apply(m, event.ToolFinished{ID: "t1", Name: "bash"})
	// 第二次调用：1 秒后出字，生成 1 秒。
	clock = clock.Add(time.Second)
	apply(m, event.MessageDelta{Text: "好了"})
	clock = clock.Add(time.Second)
	apply(m, event.MessageCompleted{Content: "好了"})
	apply(m, event.Usage{InputTokens: 1000, OutputTokens: 100, CacheReadTokens: 800})
	// 子代理的用量要算钱，但不是这一轮主对话的 token。
	apply(m, event.Subagent{CallID: "t1", Agent: "explore", Event: event.Usage{InputTokens: 99999, OutputTokens: 9999}})
	apply(m, event.TurnFinished{Reason: event.ReasonStop})

	lines := statsLines(m)
	want := "耗时 10s · 首字 1.0s · 66.7 token/s · 输入 400 · 输出 200 · 缓存 1.6k/0 · 合计 2.2k"
	if len(lines) != 1 || lines[0] != want {
		t.Fatalf("统计行 = %q\n期望 %q", lines, want)
	}
	if last := m.entries[len(m.entries)-1]; last.kind != kindStats {
		t.Error("统计行应在本轮回答的最下面")
	}
	mustContain(t, plain(m), want)
}

func TestTurnStatsAreResetBetweenTurns(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 200, 40)
	for i := 0; i < 2; i++ {
		m.stats = turnStats{start: m.now()}
		apply(m, event.MessageCompleted{Content: "答"})
		apply(m, event.Usage{InputTokens: 100, OutputTokens: 10})
		apply(m, event.TurnFinished{Reason: event.ReasonStop})
	}
	lines := statsLines(m)
	if len(lines) != 2 || !strings.Contains(lines[1], "合计 110") {
		t.Fatalf("每轮只统计自己的调用: %q", lines)
	}
	// 非流式服务没有增量：算不出首字和速度，就不写，不编 0。
	if strings.Contains(lines[0], "首字") || strings.Contains(lines[0], "token/s") || strings.Contains(lines[0], "缓存") {
		t.Errorf("缺的数据项应省略: %q", lines[0])
	}
}

func TestTurnStatsSkippedWithoutUsage(t *testing.T) {
	// 模型还没回应就出错或被取消：没有用量可报，不显示一行只有耗时的统计。
	m := newSized(t, Options{Runner: scriptRunner()}, 200, 40)
	m.stats = turnStats{start: m.now()}
	apply(m, event.Error{Message: "连不上"})
	apply(m, event.TurnFinished{Reason: event.ReasonError})
	if lines := statsLines(m); len(lines) != 0 {
		t.Errorf("没有用量时不该有统计行: %q", lines)
	}
}

func TestTurnStatsOnResumeShowTokensOnly(t *testing.T) {
	// 会话记录里没有当初的耗时，恢复出来的每轮只写 token。
	m := newSized(t, Options{Runner: scriptRunner()}, 200, 40)
	m.replay([]event.Event{
		event.TurnStarted{Prompt: "第一问"},
		event.MessageCompleted{Content: "第一答"},
		event.Usage{InputTokens: 300, OutputTokens: 20, CacheReadTokens: 100},
		event.TurnFinished{Reason: event.ReasonStop},
		event.TurnStarted{Prompt: "第二问"},
		event.MessageCompleted{Content: "第二答"},
		event.Usage{InputTokens: 500, OutputTokens: 30},
		event.TurnFinished{Reason: event.ReasonStop},
	})
	lines := statsLines(m)
	want := []string{"输入 200 · 输出 20 · 缓存 100/0 · 合计 320", "输入 500 · 输出 30 · 合计 530"}
	if len(lines) != 2 || lines[0] != want[0] || lines[1] != want[1] {
		t.Fatalf("恢复后的统计行 = %q，期望 %q", lines, want)
	}
}
