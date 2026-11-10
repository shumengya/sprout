package tui

import (
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// ordered 返回按文档顺序排列的选区两端（拖动方向可能是反的）。
func (s selection) ordered() (lo, hi pos) {
	a, h := s.anchor, s.head
	if h.line < a.line || (h.line == a.line && h.col < a.col) {
		return h, a
	}
	return a, h
}

// active 表示有需要显示和复制的选区：只有真的拖动过才算，单纯点击不算。
func (s selection) active() bool { return s.moved }

// cellSpan 返回 line 里覆盖第 col 列的那个字符所占的 [from, to) 列。
// 中文占两列，点在任意一半都会选中整个字，不会把字劈开；col 在行尾之后时返回行宽。
func cellSpan(line string, col int) (from, to int) {
	if col >= pastEnd {
		w := ansi.StringWidth(line)
		return w, w
	}
	x := 0
	for rest := line; rest != ""; {
		var cluster string
		cluster, rest, _, _ = uniseg.FirstGraphemeClusterInString(rest, -1)
		w := ansi.StringWidth(cluster)
		if col < x+w {
			return x, x + w
		}
		x += w
	}
	return x, x
}

// span 返回第 i 行被选中的 [from, to) 列；这一行没有被选中的内容时 ok 为 false。
// 选区两端各包含光标所在的那个字符。
func (m *Model) span(i int) (from, to int, ok bool) {
	if !m.sel.active() {
		return 0, 0, false
	}
	lo, hi := m.sel.ordered()
	lines := m.plainLines()
	if i < lo.line || i > hi.line || i >= len(lines) {
		return 0, 0, false
	}
	from, to = 0, ansi.StringWidth(lines[i])
	if i == lo.line {
		from, _ = cellSpan(lines[i], lo.col)
	}
	if i == hi.line {
		_, to = cellSpan(lines[i], hi.col)
	}
	return from, to, from < to
}

// selectedText 返回选中的纯文本：去掉样式，每行去掉行尾空白（表格和折行会补空格），首尾的空行也去掉。
// 行首的缩进保留，代码缩进是内容的一部分。
func (m *Model) selectedText() string {
	if !m.sel.active() {
		return ""
	}
	lo, hi := m.sel.ordered()
	lines := m.plainLines()
	var out []string
	for i := lo.line; i <= hi.line && i < len(lines); i++ {
		seg := ""
		if from, to, ok := m.span(i); ok {
			seg = strings.TrimRight(ansi.Cut(lines[i], from, to), " ")
		}
		out = append(out, seg)
	}
	text := strings.Trim(strings.Join(out, "\n"), "\n")
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return text
}

// body 是对话区的画面：viewport 的输出，再把选中的部分反色显示。
func (m *Model) body() string {
	view := m.vp.View()
	if !m.sel.active() {
		return view
	}
	rows := strings.Split(view, "\n")
	for y, row := range rows {
		if from, to, ok := m.span(m.vp.YOffset() + y); ok {
			rows[y] = reverseRange(row, from, to)
		}
	}
	return strings.Join(rows, "\n")
}

// reverseRange 把 row 的 [from, to) 列反色。被反色的部分先去掉原有样式，
// 否则里面的复位序列会让反色中途失效；两侧保持原样。
func reverseRange(row string, from, to int) string {
	head := ansi.Cut(row, 0, from)
	mid := ansi.Strip(ansi.Cut(row, from, to))
	tail := ansi.TruncateLeft(row, to, "")
	return head + styReverse.Render(mid) + tail
}

// syncSel 在对话内容变化之前调用：选区用内容坐标记录，只在内容仍然对得上时才有意义。
// 选区末端之前的行有任何变化（窗口缩放导致重新折行、/think 展开、恢复会话）就取消选区，
// 否则高亮和复制的会是错位的文字。流式输出只往末尾追加，不会影响靠前的选区。
func (m *Model) syncSel(next string) {
	if !m.sel.dragging && !m.sel.moved {
		return
	}
	_, hi := m.sel.ordered()
	if !sameHead(m.content, next, hi.line+1) {
		m.clearSel()
	}
}

// sameHead 报告 a 和 b 的前 n 行是否完全相同（不足 n 行时要求行数也相同）。
func sameHead(a, b string, n int) bool {
	pa, pb := strings.SplitN(a, "\n", n+1), strings.SplitN(b, "\n", n+1)
	if len(pa) > n {
		pa = pa[:n]
	}
	if len(pb) > n {
		pb = pb[:n]
	}
	return slices.Equal(pa, pb)
}
