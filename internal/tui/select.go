package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/x/ansi"
)

// 开启鼠标上报后终端自带的拖选就失效了（鼠标事件都交给了程序），所以对话区和输入框的拖选、复制
// 都由程序自己做：按住左键拖动选中文字，松开自动复制；在对话区拖到上下边缘还会自动滚动。
// 输入框那段直接转发给 bubbles textarea 自带的选区 API，屏幕行换算成输入框内的行即可。

var (
	styReverse = lipgloss.NewStyle().Reverse(true)
	styOK      = lipgloss.NewStyle().Foreground(lipgloss.Color("2")) // 状态行里表示成功（如已复制）
)

// pos 是对话内容里的一个位置：行号是内容行（不是屏幕行），所以滚动时选区跟着内容走。
type pos struct{ line, col int }

type selection struct {
	dragging bool
	moved    bool // 真的拖动过；单纯点击不算选中
	anchor   pos
	head     pos
	// row 是按下时点中的内容行（未收敛，可能超出内容范围）；单纯点击时用它找被点的条目。
	row int
	// dismiss 表示按下时屏幕上还有选区：这次点击只用来取消选区，不展开或收起条目。
	dismiss bool
}

// copiedMsg 是写剪贴板的结果。
type copiedMsg struct {
	n   int
	err error
}

func (m *Model) clearSel() { m.sel = selection{} }

// plainLines 返回对话内容去掉样式后的逐行文本。内容没变时复用缓存。
func (m *Model) plainLines() []string {
	if m.plain == nil || m.plainSrc != m.content {
		m.plain, m.plainSrc = strings.Split(ansi.Strip(m.content), "\n"), m.content
	}
	return m.plain
}

// pastEnd 表示“该行最后一个字符之后”。点在内容下方的空白处时用它，这样从空白处向上拖也能选到最后一行末尾。
const pastEnd = 1 << 30

// posAt 把屏幕坐标换算成内容位置。超出内容范围时收敛到最近的有效位置。
func (m *Model) posAt(x, y int) pos {
	lines := m.plainLines()
	switch row := m.vp.YOffset() + y; {
	case row < 0:
		return pos{}
	case row >= len(lines):
		return pos{line: len(lines) - 1, col: pastEnd}
	default:
		return pos{line: row, col: max(x, 0)}
	}
}

// inputTop 是输入框第一行在屏幕上的行号，供鼠标坐标换算。
// 布局见 View：对话区、活动行（可选）、候选列表（可选）、分隔线，然后才是输入框。
func (m *Model) inputTop() int {
	n := m.vp.Height() + 1 // 对话区 + 输入框上方的分隔线
	if m.activityLine() != "" {
		n++
	}
	if _, overlaid := m.overlay(); !overlaid {
		n += m.suggestRows()
	}
	return n
}

// inInput 报告屏幕第 y 行是否落在输入框里（不含被选择器替换的情况）。
func (m *Model) inInput(y int) bool {
	if _, overlaid := m.overlay(); overlaid {
		return false
	}
	top := m.inputTop()
	return y >= top && y < top+m.in.Height()
}

func (m *Model) onMouseClick(msg tea.MouseClickMsg) {
	dismiss := m.sel.active() // 先记下：按下前屏幕上有选区时，这次点击只用来取消它
	m.clearSel()
	m.inSel = false
	m.in.ClearSelection()
	if msg.Button != tea.MouseLeft {
		return // 右键等不开始拖选
	}
	if m.inInput(msg.Y) {
		if m.in.Value() == "" {
			return // 空输入框里显示的是占位提示，不是可复制的文字
		}
		m.inSel = true
		m.in.BeginSelection(msg.X, msg.Y-m.inputTop())
		return
	}
	if msg.Y < 0 || msg.Y >= m.vp.Height() {
		return // 状态行、信息栏等别处：不选任何东西
	}
	p := m.posAt(msg.X, msg.Y)
	m.sel = selection{dragging: true, anchor: p, head: p, row: m.vp.YOffset() + msg.Y, dismiss: dismiss}
}

func (m *Model) onMouseMotion(msg tea.MouseMotionMsg) {
	if m.inSel {
		// 拖出输入框上下边界时 PositionAt 会收敛到首/尾，不必单独限制。
		m.in.ExtendSelection(msg.X, msg.Y-m.inputTop())
		return
	}
	if !m.sel.dragging {
		return
	}
	// 拖到对话区上下边缘之外：每个事件滚一行，让用户可以选中超出一屏的内容。
	switch {
	case msg.Y >= m.vp.Height():
		m.scrollBy(1)
		msg.Y = m.vp.Height() - 1
	case msg.Y < 0:
		m.scrollBy(-1)
		msg.Y = 0
	}
	if p := m.posAt(msg.X, msg.Y); p != m.sel.head {
		m.sel.head, m.sel.moved = p, true
	}
}

// onMouseRelease 结束拖动。真的拖选过就复制；单纯点击用来展开或收起思考过程（或只是取消选区）。
func (m *Model) onMouseRelease() tea.Cmd {
	if m.inSel {
		m.inSel = false
		m.in.EndSelection() // 宽度为 0 的选区（只是点击）会被丢弃
		if text := m.in.SelectedText(); text != "" {
			return m.copyCmd(text)
		}
		return nil
	}
	if !m.sel.dragging {
		return nil
	}
	m.sel.dragging = false
	if !m.sel.moved {
		row, dismiss := m.sel.row, m.sel.dismiss
		m.clearSel()
		if !dismiss {
			m.toggleThinkAt(row)
		}
		return nil
	}
	text := m.selectedText()
	if text == "" {
		m.clearSel()
		return nil
	}
	return m.copyCmd(text)
}

// copyCmd 同时走两条路：OSC 52（终端负责写剪贴板，SSH 里也能用）和本机系统剪贴板。
// 两条路都不保证成功且没法互相确认，所以任何一条出错都只是提示，不当作致命。
func (m *Model) copyCmd(text string) tea.Cmd {
	write := m.clip
	return tea.Batch(tea.SetClipboard(text), func() tea.Msg {
		return copiedMsg{n: utf8.RuneCountInString(text), err: write(text)}
	})
}

func (m *Model) onCopied(msg copiedMsg) {
	m.statusOK = msg.err == nil
	if msg.err == nil {
		m.status = fmt.Sprintf("已复制 %d 个字符", msg.n)
		return
	}
	m.status = "系统剪贴板写入失败（终端支持 OSC 52 的话可能仍已复制）：" + msg.err.Error()
}

// defaultClip 写本机系统剪贴板。纯 Go 实现，无 CGO；Linux 需要 xclip/xsel/wl-clipboard，Termux 用 termux-clipboard-set。
func defaultClip(s string) error { return clipboard.WriteAll(s) }
