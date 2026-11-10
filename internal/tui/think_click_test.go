package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shumengya/sprout/internal/agent/event"
)

// thoughtModel 返回一个已结束一轮的 Model：一段多行思考，后面是回答。剪贴板换成替身。
func thoughtModel(t *testing.T, w, h int) (*Model, *fakeClip) {
	t.Helper()
	m := newSized(t, Options{Runner: scriptRunner()}, w, h)
	fc := &fakeClip{}
	m.clip = fc.write
	begin(m, "q")
	apply(m, event.ReasoningDelta{Text: "第一行思考\n第二行思考\n第三行思考"},
		event.MessageDelta{Text: "最终回答"},
		event.MessageCompleted{Content: "最终回答", Reasoning: "第一行思考\n第二行思考\n第三行思考"},
		event.TurnFinished{Reason: event.ReasonStop})
	return m, fc
}

// click 模拟一次原地点击（按下后直接松开），返回松开时产生的命令。
func click(m *Model, x, y int) tea.Cmd {
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	_, cmd := m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x, Y: y})
	return cmd
}

// thinkRow 返回思考条目头部所在的屏幕行。
func thinkRow(t *testing.T, m *Model) int {
	t.Helper()
	_, y := selWhere(t, m, "已思考")
	return y
}

// transcript 返回对话内容的纯文本。跨行断言用它：plain(m) 是整屏画面，viewport 会把每行补空格到窗口宽度。
func transcript(m *Model) string { return strings.Join(m.plainLines(), "\n") }

func TestClickThoughtExpandsAndClickAgainCollapses(t *testing.T) {
	m, fc := thoughtModel(t, 80, 30)
	mustNotContain(t, plain(m), "  第二行思考")

	if cmd := click(m, 3, thinkRow(t, m)); cmd != nil {
		t.Fatal("点击不应产生复制命令")
	}
	mustContain(t, transcript(m), "  第一行思考\n  第二行思考\n  第三行思考")

	// 点展开后的正文也能收起，不一定要点头部。
	_, y := selWhere(t, m, "第三行思考")
	click(m, 4, y)
	s := plain(m)
	mustContain(t, s, "已思考", "：第一行思考 第二行思考 第三行思考")
	mustNotContain(t, s, "  第二行思考")
	if len(fc.got) != 0 {
		t.Fatalf("点击不应写剪贴板: %q", fc.got)
	}
}

func TestClickOtherEntriesDoesNothing(t *testing.T) {
	m, _ := thoughtModel(t, 80, 30)
	before := plain(m)
	_, y := selWhere(t, m, "最终回答")
	click(m, 2, y)
	click(m, 2, thinkRow(t, m)-1) // 条目之间的空行
	click(m, 2, m.vp.Height()-1)  // 内容下方的空白
	if plain(m) != before {
		t.Fatal("点在思考条目之外不应改变画面")
	}
}

func TestClickDismissingSelectionDoesNotToggle(t *testing.T) {
	m, fc := thoughtModel(t, 80, 30)
	x, y := selWhere(t, m, "最终回答")
	selDrag(m, x, y, x+3, y)
	if len(fc.got) != 1 {
		t.Fatalf("拖选应复制: %q", fc.got)
	}

	// 有选区时点在思考条目上：只取消选区，不展开。
	click(m, 3, thinkRow(t, m))
	if m.sel != (selection{}) {
		t.Fatal("点击应取消选区")
	}
	mustNotContain(t, plain(m), "  第二行思考")

	click(m, 3, thinkRow(t, m))
	mustContain(t, plain(m), "  第二行思考")
}

func TestDragAcrossThoughtCopiesInsteadOfToggling(t *testing.T) {
	m, fc := thoughtModel(t, 80, 30)
	y := thinkRow(t, m)
	selDrag(m, 0, y, 8, y)
	if len(fc.got) != 1 || !strings.HasPrefix(fc.got[0], "已思考") {
		t.Fatalf("拖选思考条目应复制文字: %q", fc.got)
	}
	mustNotContain(t, plain(m), "  第二行思考")
}

func TestLiveThoughtIgnoresClicks(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 30)
	begin(m, "q")
	apply(m, event.ReasoningDelta{Text: "还在想"})
	_, y := selWhere(t, m, "思考中")
	click(m, 2, y)
	if e := m.think; e == nil || e.open {
		t.Fatal("流式中的思考不响应点击")
	}
}

func TestToggleKeepsClickedThoughtInPlace(t *testing.T) {
	// 思考条目上下都有很多内容，展开和收起时被点中的头部不能在屏幕上跳走。
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 20)
	fillTranscript(m, 40)
	begin(m, "q")
	long := strings.Repeat("很长的一行思考内容\n", 30)
	apply(m, event.MessageCompleted{Content: "答", Reasoning: long})
	fillTranscript(m, 40)
	m.Update(keyOf(tea.KeyHome, tea.ModCtrl))
	for !strings.Contains(m.vp.View(), "已思考") {
		m.scrollBy(1)
	}
	y := thinkRow(t, m)

	click(m, 2, y)
	if got := thinkRow(t, m); got != y {
		t.Fatalf("展开后头部从第 %d 行跳到第 %d 行", y, got)
	}
	if m.unseen {
		t.Error("自己展开造成的行数变化不算“有新内容”")
	}
	click(m, 2, y)
	if got := thinkRow(t, m); got != y {
		t.Fatalf("收起后头部从第 %d 行跳到第 %d 行", y, got)
	}
}

func TestCollapsingFromInsideLongThoughtBringsHeaderToTop(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 20)
	fillTranscript(m, 40)
	begin(m, "q")
	apply(m, event.MessageCompleted{Content: "答", Reasoning: strings.Repeat("思考内容\n", 50)})
	fillTranscript(m, 40)
	submit(m, "/think") // 全部展开
	m.Update(keyOf(tea.KeyHome, tea.ModCtrl))
	for !strings.Contains(m.vp.View(), "已思考") {
		m.scrollBy(1)
	}
	m.scrollBy(thinkRow(t, m) + 5) // 头部滚出屏幕上方 5 行，屏幕上只剩正文
	mustNotContain(t, m.vp.View(), "已思考")

	click(m, 4, 3)
	if y := thinkRow(t, m); y != 0 {
		t.Fatalf("收起后头部应在对话区顶部，实际在第 %d 行", y)
	}
}

func TestThinkCommandSetsDefaultForNewThoughts(t *testing.T) {
	m, _ := thoughtModel(t, 80, 40)
	submit(m, "/think")
	begin(m, "q2")
	apply(m, event.ReasoningDelta{Text: "新的思考"}, event.MessageDelta{Text: "答"})
	mustContain(t, transcript(m), "\n  新的思考")
}
