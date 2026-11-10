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

// 开启鼠标上报后终端自带的拖选就失效了（鼠标事件都交给了程序），所以对话区的拖选和复制由程序自己做：
// 在对话区按住左键拖动选中文字，松开自动复制；拖到上下边缘会自动滚动。

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

func (m *Model) onMouseClick(msg tea.MouseClickMsg) {
	if msg.Button != tea.MouseLeft || msg.Y < 0 || msg.Y >= m.vp.Height() {
		m.clearSel() // 点在输入框、状态行等别处：取消选区
		return
	}
	p := m.posAt(msg.X, msg.Y)
	m.sel = selection{dragging: true, anchor: p, head: p, row: m.vp.YOffset() + msg.Y, dismiss: m.sel.active()}
}

func (m *Model) onMouseMotion(msg tea.MouseMotionMsg) {
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
