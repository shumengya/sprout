package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/shumengya/sprout/internal/agent/event"
)

// fakeClock 是可以手动拨动的时钟。测试替身，只出现在 _test.go 里。
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time      { return c.t }
func (c *fakeClock) add(d time.Duration) { c.t = c.t.Add(d) }

func withClock(m *Model) *fakeClock {
	c := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	m.now = c.now
	return c
}

func TestThoughtHeadShowsDurationAndReportedTokens(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 30)
	clk := withClock(m)
	begin(m, "q")
	apply(m, event.ReasoningDelta{Text: "先想"})
	clk.add(12400 * time.Millisecond)
	apply(m, event.ReasoningDelta{Text: "再想"}, event.MessageDelta{Text: "答"},
		event.MessageCompleted{Content: "答", Reasoning: "先想再想"},
		event.Usage{TotalTokens: 400, ReasoningTokens: 340})
	mustContain(t, plain(m), "已思考 12秒 340token：先想再想")
	mustNotContain(t, plain(m), "约")
}

func TestReasoningTokensOnlyAttachToTheirOwnCall(t *testing.T) {
	// 第二次调用没有思考：它的 Usage 不能把数字填到第一次调用的思考条目上。
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 30)
	begin(m, "q")
	apply(m,
		event.MessageCompleted{Reasoning: "第一次的思考", ToolCalls: []event.ToolCall{{ID: "c", Name: "bash"}}},
		requested("c", "bash", `{}`), event.ToolFinished{ID: "c", Name: "bash"},
		event.MessageCompleted{Content: "答"},
		event.Usage{TotalTokens: 99, ReasoningTokens: 77},
	)
	s := plain(m)
	mustContain(t, s, "已思考 约6token：第一次的思考")
	mustNotContain(t, s, "77token")
}

func TestUsageNotRightAfterCompletionIsNotAttached(t *testing.T) {
	// Usage 只属于紧挨在它前面的 MessageCompleted；中间隔了别的事件就不能再挂到那次的思考上。
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 30)
	begin(m, "q")
	apply(m,
		event.MessageCompleted{Reasoning: "想一下", ToolCalls: []event.ToolCall{{ID: "c", Name: "bash"}}},
		requested("c", "bash", `{}`),
		event.Usage{TotalTokens: 99, ReasoningTokens: 77},
	)
	mustNotContain(t, plain(m), "77token")
	mustContain(t, plain(m), "已思考 约3token：想一下")
}

func TestReasoningTokensSurviveResume(t *testing.T) {
	// 恢复会话时由完整消息重建：没有流式增量所以没有耗时，但服务端报告过的 token 数还在。
	m := newSized(t, Options{Runner: scriptRunner(), Replay: []event.Event{
		event.TurnStarted{Prompt: "问"},
		event.MessageCompleted{Content: "答", Reasoning: "恢复的思考"},
		event.Usage{TotalTokens: 60, ReasoningTokens: 42},
		event.TurnFinished{Reason: event.ReasonStop},
	}}, 80, 30)
	mustContain(t, plain(m), "已思考 42token：恢复的思考")
}

func TestCollapsedThoughtIsOneTruncatedLine(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 40, 30)
	begin(m, "q")
	apply(m, event.MessageCompleted{Content: "答", Reasoning: strings.Repeat("很长的思考内容\n", 20)})
	var line string
	for _, l := range m.plainLines() {
		if strings.HasPrefix(l, "已思考") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("找不到思考条目:\n%s", strings.Join(m.plainLines(), "\n"))
	}
	if w := ansi.StringWidth(line); w > 40 || !strings.HasSuffix(line, "…") {
		t.Fatalf("应截成一行并以省略号结尾（宽 %d）: %q", w, line)
	}
	if strings.Count(strings.Join(m.plainLines(), "\n"), "很长的思考内容") > 3 {
		t.Error("折叠时不应显示全文")
	}
}

func TestEmptyThoughtHasNoTrailingColon(t *testing.T) {
	e := &entry{kind: kindThink, thinkDur: 2 * time.Second}
	if got := ansi.Strip(e.drawThink(40)); got != "已思考 2秒" {
		t.Errorf("got %q", got)
	}
}

func TestFmtThinkDur(t *testing.T) {
	for d, want := range map[time.Duration]string{
		300 * time.Millisecond:  "1秒",
		1400 * time.Millisecond: "1秒",
		1600 * time.Millisecond: "2秒",
		59 * time.Second:        "59秒",
		60 * time.Second:        "1分00秒",
		125 * time.Second:       "2分05秒",
	} {
		if got := fmtThinkDur(d); got != want {
			t.Errorf("fmtThinkDur(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestEstimateTokens(t *testing.T) {
	for s, want := range map[string]int{"": 0, "abc": 1, "abcd": 2, "中文": 2, "中a": 2} {
		if got := estimateTokens(s); got != want {
			t.Errorf("estimateTokens(%q) = %d, want %d", s, got, want)
		}
	}
}
