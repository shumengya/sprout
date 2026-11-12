package tui

import (
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// 输入框里拖选也要能复制：拖选期间鼠标事件转给 textarea，松开时写入剪贴板。
func TestDragSelectsAndCopiesInsideInput(t *testing.T) {
	m, fc := selModel(t, "对话内容")
	m.in.SetValue("hello banana split")
	m.Update(tea.PasteMsg{Content: m.in.Value()}) // 让高度与内容同步
	top := m.inputTop()

	// "❯ " 占两列：hello 之后是第 8 列（0 起）。
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 8, Y: top})
	// textarea 把坐标点所在的那个字符留给光标本身，选区止于该字符之前，所以多拖一列才算到 a。
	m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: 14, Y: top})
	_, cmd := m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: 14, Y: top})
	selSettle(m, cmd)

	if !slices.Equal(fc.got, []string{"banana"}) {
		t.Fatalf("复制的输入框内容 = %q", fc.got)
	}
	if m.sel != (selection{}) {
		t.Error("输入框的拖选不应影响对话区的选区")
	}
	mustContain(t, plain(m), "已复制 6 个字符")
}

// 输入框为空时点进去（显示的是占位提示）不应产生选区或复制。
func TestClickEmptyInputCopiesNothing(t *testing.T) {
	m, fc := selModel(t, "对话内容")
	top := m.inputTop()
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 4, Y: top})
	m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: 10, Y: top})
	_, cmd := m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: 10, Y: top})
	selSettle(m, cmd)
	if len(fc.got) != 0 {
		t.Fatalf("空输入框不应复制: %q", fc.got)
	}
	if m.inSel {
		t.Error("空输入框不应进入拖选状态")
	}
}

// 点输入框会取消对话区已有的选区（和点状态行一样）。
func TestClickInputClearsTranscriptSelection(t *testing.T) {
	m, _ := selModel(t, "hello banana split today")
	x, y := selWhere(t, m, "banana")
	selDrag(m, x, y, x+5, y)
	if !m.sel.active() {
		t.Fatal("前置条件：对话区应已有选区")
	}
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 1, Y: m.inputTop()})
	if m.sel != (selection{}) {
		t.Error("点输入框应取消对话区选区")
	}
}
