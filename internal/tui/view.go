package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// View 渲染整个界面：header、transcript、状态行、输入框。
func (m *Model) View() tea.View {
	var v tea.View
	v.AltScreen = true
	v.WindowTitle = "Sprout"
	if m.w <= 0 || m.h <= 0 {
		v.Content = "加载中…"
		return v
	}

	sep := styFaint.Render(strings.Repeat("─", m.w))
	top := m.header() + "\n" + sep
	body := m.vp.View()
	footer := sep + "\n" + m.statusLine() + "\n" + m.in.View()
	v.Content = top + "\n" + body + "\n" + footer

	// 把输入框的光标位置换算到整个界面里。输入框在最下面 inputH 行。
	if c := m.in.Cursor(); c != nil {
		c.Y += headerH + m.vp.Height() + footerH - inputH
		v.Cursor = c
	}
	return v
}

func (m *Model) header() string {
	info := m.opts.Info
	names := make([]string, len(info.Tools))
	for i, t := range info.Tools {
		names[i] = t.Name
	}
	tools := "无"
	if len(names) > 0 {
		tools = strings.Join(names, ", ")
	}
	lines := []string{
		styTitle.Render("Sprout") + "  model: " + info.Model,
		"cwd: " + info.Cwd + "   data: " + info.DataDir,
		"tools: " + tools,
	}
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, m.w, "…")
	}
	return strings.Join(lines, "\n")
}

func (m *Model) statusLine() string {
	var left string
	switch {
	case m.status != "":
		left = styWarn.Render(m.status)
	case m.running && m.curTool != "":
		left = m.sp.View() + " 执行 " + m.curTool
	case m.running:
		left = m.sp.View() + " 生成中"
	default:
		left = styFaint.Render("空闲")
	}
	right := "Ctrl+C 退出"
	if m.running {
		right = "Ctrl+C 取消"
	}
	if m.tokens > 0 {
		right = fmt.Sprintf("%d tokens  %s", m.tokens, right)
	}
	right = styFaint.Render(right)

	left = ansi.Truncate(left, max(m.w-ansi.StringWidth(right)-1, 1), "…")
	gap := max(m.w-ansi.StringWidth(left)-ansi.StringWidth(right), 1)
	return left + strings.Repeat(" ", gap) + right
}
