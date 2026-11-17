package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// SessionTree 是当前会话的轮次树：所有分支都在同一个会话文件里。
type SessionTree struct {
	Nodes   []TreeNode // 按轮号升序
	Current int        // 当前位置：下一次提问接在第几轮之后，0 是会话开头
}

// TreeNode 是树上的一轮。
type TreeNode struct {
	Turn   int // 从 1 起
	Parent int // 接在第几轮之后；0 是会话开头
	Prompt string
	Reply  string // 最后一段文字回复，已压成一行
	Images int
	Time   time.Time
}

// treeRow 是摊平后显示的一行。
type treeRow struct {
	node   TreeNode
	prefix string // 树枝前缀（│ ├─ └─）
	onPath bool   // 在从会话开头到当前位置的路径上
}

// treePicker 是 /tree 面板：替换输入框，↑↓ 选择，Enter 从选中那轮之后继续，e 改问选中那轮。
type treePicker struct {
	rows    []treeRow
	current int
	cursor  int
	offset  int
	loading bool
}

// flattenTree 把轮次树摊平成显示顺序。只在分叉处缩进，没有分叉的一串轮次保持同一列；
// 每个分叉处先列出通向当前位置的那一支，和 pi 的 /tree 一致。
func flattenTree(t *SessionTree) []treeRow {
	children := map[int][]TreeNode{}
	parent := map[int]int{}
	for _, n := range t.Nodes {
		children[n.Parent] = append(children[n.Parent], n)
		parent[n.Turn] = n.Parent
	}
	onPath := map[int]bool{}
	for turn := t.Current; turn > 0; turn = parent[turn] {
		onPath[turn] = true
	}
	ordered := func(turn int) []TreeNode {
		cs := children[turn]
		for i, c := range cs {
			if onPath[c.Turn] && i > 0 {
				cs = append([]TreeNode{c}, append(append([]TreeNode(nil), cs[:i]...), cs[i+1:]...)...)
				break
			}
		}
		return cs
	}

	var rows []treeRow
	// gutter 是祖先分叉留下的竖线，branch 是本节点的连接符；单子节点的链条沿用同样的缩进。
	var walk func(n TreeNode, gutter, branch string)
	walk = func(n TreeNode, gutter, branch string) {
		rows = append(rows, treeRow{node: n, prefix: gutter + branch, onPath: onPath[n.Turn]})
		switch branch {
		case "├─ ":
			gutter += "│  "
		case "└─ ":
			gutter += "   "
		}
		cs := ordered(n.Turn)
		if len(cs) == 1 {
			walk(cs[0], gutter, "")
			return
		}
		for i, c := range cs {
			b := "├─ "
			if i == len(cs)-1 {
				b = "└─ "
			}
			walk(c, gutter, b)
		}
	}
	roots := ordered(0)
	for i, r := range roots {
		b := ""
		if len(roots) > 1 { // 从会话开头就分了叉：顶层也画连接符
			b = "├─ "
			if i == len(roots)-1 {
				b = "└─ "
			}
		}
		walk(r, "", b)
	}
	return rows
}

// treeLoadedMsg 是读完会话树后发回 Update 的消息。
type treeLoadedMsg struct {
	tree *SessionTree
	err  error
}

// treeCmd 处理 /tree：异步读出会话树，读完再打开面板。
func (m *Model) treeCmd() tea.Cmd {
	s := m.opts.Sessions
	switch {
	case s == nil:
		m.add(&entry{kind: kindNotice, text: "当前不能查看会话树（没有可用的 agent）。"})
		return nil
	case m.busy():
		m.status = "正在生成、恢复、切换模型或压缩，暂时不能切换分支"
		return nil
	}
	m.treePick = &treePicker{loading: true}
	m.status = ""
	return func() tea.Msg {
		t, err := s.Tree()
		return treeLoadedMsg{t, err}
	}
}

func (m *Model) onTreeLoaded(msg treeLoadedMsg) {
	p := m.treePick
	if p == nil {
		return // 读完之前面板已经关了
	}
	if msg.err != nil {
		m.treePick = nil
		m.add(&entry{kind: kindNotice, text: "没有可切换的分支：" + msg.err.Error()})
		return
	}
	p.loading, p.rows, p.current = false, flattenTree(msg.tree), msg.tree.Current
	for i, r := range p.rows {
		if r.node.Turn == p.current {
			p.cursor = i
		}
	}
}

// treeKey 在面板打开时处理按键，返回 true 表示已消费。
func (m *Model) treeKey(k tea.KeyPressMsg) (bool, tea.Cmd) {
	p := m.treePick
	if p == nil {
		return false, nil
	}
	key := k.String()
	if key == "esc" || key == "ctrl+c" {
		m.treePick = nil
		m.status = "已关闭会话树"
		return true, nil
	}
	if p.loading || len(p.rows) == 0 {
		return true, nil
	}
	switch key {
	case "up", "ctrl+p", "k":
		p.cursor--
	case "down", "ctrl+n", "j":
		p.cursor++
	case "pgup":
		p.cursor -= m.treeRows()
	case "pgdown":
		p.cursor += m.treeRows()
	case "home":
		p.cursor = 0
	case "end":
		p.cursor = len(p.rows) - 1
	case "enter":
		n := p.rows[p.cursor].node
		if n.Turn == p.current {
			m.treePick = nil
			m.status = "已经在这一轮之后了"
			return true, nil
		}
		return true, m.navigate(n.Turn, "")
	case "e": // 改问：回到这一轮提问之前，原提问放进输入框
		n := p.rows[p.cursor].node
		return true, m.navigate(n.Parent, n.Prompt)
	}
	p.cursor = min(max(p.cursor, 0), len(p.rows)-1)
	return true, nil
}

// navigate 关闭面板并异步切换到第 turn 轮之后；结果和恢复会话一样用 resumeDoneMsg 送回。
func (m *Model) navigate(turn int, edit string) tea.Cmd {
	s := m.opts.Sessions
	m.treePick = nil
	m.resuming = true
	m.status = "正在切换分支…"
	return func() tea.Msg {
		r, err := s.Navigate(turn)
		return resumeDoneMsg{res: r, err: err, failText: "切换分支失败：", edit: edit}
	}
}

// treeRows 是面板列表最多显示的行数：屏幕越高显示越多，3 到 16 行。
func (m *Model) treeRows() int { return min(max(m.h-12, 3), 16) }

// treeView 渲染会话树面板，替换输入框的位置。
func (m *Model) treeView(w int) string {
	p := m.treePick
	lines := []string{
		styBrowseTitle.Render("会话树"),
		styFaint.Render("↑↓ 选择 · Enter 从这一轮之后继续 · e 改问这一轮 · Esc 关闭；原来的分支都会保留"),
	}
	if p.loading {
		lines = append(lines, styFaint.Render("  正在读取…"))
		return strings.Join(lines, "\n")
	}
	height := m.treeRows()
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+height {
		p.offset = p.cursor - height + 1
	}
	p.offset = max(min(p.offset, len(p.rows)-height), 0)
	for i := p.offset; i < min(p.offset+height, len(p.rows)); i++ {
		lines = append(lines, p.row(p.rows[i], i == p.cursor, w))
	}
	lines = append(lines, styFaint.Render(fmt.Sprintf("(%d/%d)", p.cursor+1, len(p.rows))))
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, w, "…")
	}
	return strings.Join(lines, "\n")
}

// row 渲染一轮：树枝前缀、提问，后面跟着回复的开头。不在当前路径上的分支淡显。
func (p *treePicker) row(r treeRow, sel bool, w int) string {
	text := oneLineTitle(r.node.Prompt, 200)
	if r.node.Images > 0 {
		text = fmt.Sprintf("[图片×%d] ", r.node.Images) + text
	}
	if text == "" {
		text = "（空提问）"
	}
	tail := ""
	if r.node.Reply != "" {
		tail = "  → " + r.node.Reply
	}
	mark := ""
	if r.node.Turn == p.current {
		mark = " ← 当前"
	}
	if sel {
		return ansi.Truncate(styReverse.Render("❯ "+r.prefix+text+mark+tail), w, "…")
	}
	body := styUser.Render(text)
	if !r.onPath {
		body = styFaint.Render(text)
	}
	return ansi.Truncate("  "+styFaint.Render(r.prefix)+body+styOK.Render(mark)+styFaint.Render(tail), w, "…")
}
