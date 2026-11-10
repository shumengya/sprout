package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shumengya/sprout/internal/agent/event"
)

func TestPlainClickDoesNotSelectOrCopy(t *testing.T) {
	m, fc := selModel(t, "hello banana split today")
	x, y := selWhere(t, m, "banana")

	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	_, cmd := m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x, Y: y})
	if cmd != nil || len(fc.got) != 0 || m.sel != (selection{}) {
		t.Fatalf("单纯点击不应选中或复制: cmd=%v got=%q sel=%+v", cmd != nil, fc.got, m.sel)
	}

	// 按下后在同一格“拖动”也不算拖选。
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: x, Y: y})
	_, cmd = m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x, Y: y})
	if cmd != nil || len(fc.got) != 0 {
		t.Fatal("没有移动就不应复制")
	}
}

func TestKeyPressClearsSelection(t *testing.T) {
	m, _ := selModel(t, "hello banana split today")
	x, y := selWhere(t, m, "banana")
	selDrag(m, x, y, x+5, y)
	if !strings.Contains(m.body(), "\x1b[7m") {
		t.Fatal("拖选后应有反色高亮")
	}
	m.Update(typed("x"))
	if m.sel != (selection{}) || strings.Contains(m.body(), "\x1b[7m") {
		t.Fatal("按键后选区应消失")
	}
}

func TestClickElsewhereOrOtherButtonClearsSelection(t *testing.T) {
	m, _ := selModel(t, "hello banana split today")
	x, y := selWhere(t, m, "banana")

	selDrag(m, x, y, x+5, y)
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 3, Y: m.vp.Height() + 1}) // 点在输入框一带
	if m.sel != (selection{}) {
		t.Error("点在对话区之外应取消选区")
	}

	selDrag(m, x, y, x+5, y)
	m.Update(tea.MouseClickMsg{Button: tea.MouseRight, X: x, Y: y})
	if m.sel != (selection{}) {
		t.Error("右键不应保留选区或开始拖选")
	}
}

func TestDragFromBlankSpaceBelowContent(t *testing.T) {
	m, fc := selModel(t, "alpha beta")
	x, y := selWhere(t, m, "beta")
	// 从内容下方的空白处向上拖到 beta：选到最后一行行尾。
	selDrag(m, 0, m.vp.Height()-3, x, y)
	if len(fc.got) != 1 || fc.got[0] != "beta" {
		t.Fatalf("复制的内容 = %q", fc.got)
	}
}

func TestDragPastEdgeScrolls(t *testing.T) {
	m := scrolled(t)
	m.clip = (&fakeClip{}).write
	m.Update(keyOf(tea.KeyHome, tea.ModCtrl))
	if m.vp.YOffset() != 0 {
		t.Fatalf("应先滚到最前: %d", m.vp.YOffset())
	}
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 3, Y: 2})
	m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: 5, Y: m.vp.Height() + 3})
	if m.vp.YOffset() != 1 || m.sel.head.line != m.vp.Height() {
		t.Fatalf("拖到下边缘之外应滚一行并选到可见区最后一行: offset=%d head=%+v", m.vp.YOffset(), m.sel.head)
	}
	m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: 5, Y: -2})
	if m.vp.YOffset() != 0 {
		t.Fatalf("拖到上边缘之外应往回滚: %d", m.vp.YOffset())
	}
}

func TestResizeClearsSelectionBecauseLinesMoved(t *testing.T) {
	m, _ := selModel(t, "alpha beta gamma")
	x, y := selWhere(t, m, "beta")
	selDrag(m, x, y, x+3, y)
	if !m.sel.active() {
		t.Fatal("应已选中")
	}
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 24}) // 开场提示重新折行，选区对不上了
	if m.sel != (selection{}) {
		t.Fatal("内容重排后选区应取消，否则会高亮和复制错位的文字")
	}
}

func TestAppendingBelowKeepsSelection(t *testing.T) {
	m, _ := selModel(t, "first paragraph\n\nsecond")
	x, y := selWhere(t, m, "first")
	selDrag(m, x, y, x+4, y)
	apply(m, event.MessageDelta{Text: " more words"}) // 流式输出只改后面的行
	if m.selectedText() != "first" {
		t.Fatalf("后面追加内容不应影响靠前的选区: %q", m.selectedText())
	}
}

func TestMouseToggleClearsSelection(t *testing.T) {
	m, _ := selModel(t, "hello banana split today")
	x, y := selWhere(t, m, "banana")
	selDrag(m, x, y, x+5, y)
	m.slash("/mouse")
	if m.sel != (selection{}) {
		t.Fatal("关闭鼠标上报时应取消选区")
	}
}

func TestCellSpan(t *testing.T) {
	for _, tc := range []struct {
		line     string
		col      int
		from, to int
	}{
		{"abc", 1, 1, 2},
		{"中文", 0, 0, 2},
		{"中文", 1, 0, 2}, // 点在字的右半边也选中整个字
		{"中文", 2, 2, 4},
		{"a中", 2, 1, 3},
		{"中文", 4, 4, 4}, // 行尾之后
		{"", 0, 0, 0},
		{"中文", pastEnd, 4, 4},
	} {
		if from, to := cellSpan(tc.line, tc.col); from != tc.from || to != tc.to {
			t.Errorf("cellSpan(%q, %d) = [%d,%d)，期望 [%d,%d)", tc.line, tc.col, from, to, tc.from, tc.to)
		}
	}
}

func TestSameHead(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		n    int
		want bool
	}{
		{"a\nb\nc", "a\nb\nX", 2, true},
		{"a\nb\nc", "a\nb\nX", 3, false},
		{"a", "a\nb", 1, true},
		{"a", "a\nb", 2, false}, // 不足 n 行时行数也要一致
		{"a\nb", "a\nb", 5, true},
	} {
		if got := sameHead(tc.a, tc.b, tc.n); got != tc.want {
			t.Errorf("sameHead(%q, %q, %d) = %v", tc.a, tc.b, tc.n, got)
		}
	}
}

func TestMouseOnUnsizedModelDoesNotPanic(t *testing.T) {
	m := New(Options{Runner: scriptRunner()}) // 还没收到窗口尺寸
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 3, Y: 3})
	m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: 5, Y: 5})
	m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: 5, Y: 5})
	if p := m.posAt(3, 3); p.line != 0 {
		t.Errorf("posAt = %+v", p)
	}
}
