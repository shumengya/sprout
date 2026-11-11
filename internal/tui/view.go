package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// View 渲染整个界面，从上到下：对话区、活动行（有事可报时才出现）、分隔线、输入框、分隔线、信息栏。
// 空闲时底部只占 4 行，上面整块留给对话。
func (m *Model) View() tea.View {
	var v tea.View
	v.AltScreen = true
	v.WindowTitle = "Sprout"
	if m.mouse {
		v.MouseMode = tea.MouseModeCellMotion
	}
	if m.w <= 0 || m.h <= 0 {
		v.Content = "加载中…"
		return v
	}

	sep := styFaint.Render(strings.Repeat("─", m.w))
	input := m.inputView()
	if m.picker != nil {
		input = m.pickerView(m.w, pickerMaxRows) // 选择器替换输入框，对话区不被遮挡
	}
	parts := []string{overlayHint(m.body(), m.scrollHint(), m.w)}
	above := m.vp.Height() // 输入框上面的行数：对话区、活动行（如果有）、分隔线
	if act := m.activityLine(); act != "" {
		parts = append(parts, act)
		above++
	}
	parts = append(parts, sep, input, sep, m.infoLine())
	above++
	v.Content = strings.Join(parts, "\n")

	// 把输入框的光标位置换算到整个界面里。选择器打开时没有输入光标。
	if m.picker == nil {
		if c := m.in.Cursor(); c != nil {
			c.Y += above
			v.Cursor = c
		}
	}
	return v
}

var (
	styInfoLabel = lipgloss.NewStyle().Faint(true)
	styInfoModel = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))  // 绿
	styInfoDir   = lipgloss.NewStyle().Foreground(lipgloss.Color("12")) // 亮蓝：普通蓝在深色背景上太暗
	styInfoTool  = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))  // 洋红，和工具卡片同色系
)

// infoLine 是最底下的信息栏：左边模型、当前目录、可用工具，用不同颜色区分；右边是本会话累计 token。
// 宽度不够时 token 数最先让步，其次是目录（从左边截断，保留末尾几级最有辨识度的部分），最后才整行截断。
func (m *Model) infoLine() string {
	info := m.opts.Info
	tools := styFaint.Render("无")
	if len(info.Tools) > 0 {
		names := make([]string, len(info.Tools))
		for i, t := range info.Tools {
			names[i] = t.Name
		}
		tools = styInfoTool.Render(strings.Join(names, "、"))
	}
	const gap = "  "
	model := styInfoLabel.Render("模型 ") + styInfoModel.Render(info.Model)
	toolPart := styInfoLabel.Render("工具 ") + tools

	// 先算出模型和工具占掉多少，剩下的给目录。
	fixed := ansi.StringWidth(model) + ansi.StringWidth(toolPart) + 2*len(gap) + ansi.StringWidth("目录 ")
	dir := truncateLeft(info.Cwd, max(m.w-fixed, 8))
	dirPart := styInfoLabel.Render("目录 ") + styInfoDir.Render(dir)
	left := ansi.Truncate(model+gap+dirPart+gap+toolPart, m.w, "…")

	if m.tokens <= 0 {
		return left
	}
	right := styFaint.Render(fmtTokens(m.tokens) + " token")
	pad := m.w - ansi.StringWidth(left) - ansi.StringWidth(right)
	if pad < len(gap) { // 放不下就不显示，左边的信息更重要
		return left
	}
	return left + strings.Repeat(" ", pad) + right
}

// truncateLeft 把 s 截到不超过 w 列，超出时从左边去掉并以 … 开头。
func truncateLeft(s string, w int) string {
	if ansi.StringWidth(s) <= w {
		return s
	}
	return ansi.TruncateLeft(s, ansi.StringWidth(s)-(w-1), "…")
}

// activityLine 是输入框上方的一行：正在做什么、临时提示。空闲且没有提示时返回空字符串，这一行就不占位置。
// 翻看历史的位置提示不放这里，而是叠在对话区最后一行（见 overlayHint）：它要是占一行，
// 上滚时对话区会变矮、底部位置跟着变，滚回去就差一行到不了底。
func (m *Model) activityLine() string {
	var left string
	switch {
	case m.status != "" && m.statusOK:
		left = styOK.Render(m.status)
	case m.status != "":
		left = styWarn.Render(m.status)
	case m.running:
		left = m.runningLabel()
	default:
		return ""
	}
	if !m.running {
		return ansi.Truncate(left, m.w, "…")
	}
	right := styFaint.Render("Ctrl+C 取消")
	left = ansi.Truncate(left, max(m.w-ansi.StringWidth(right)-1, 1), "…")
	return left + strings.Repeat(" ", max(m.w-ansi.StringWidth(left)-ansi.StringWidth(right), 1)) + right
}

// overlayHint 把翻看历史的提示叠在对话区最后一行的右端，挡住那里的文字（复制时取的仍是原文）。
func overlayHint(body, hint string, w int) string {
	if hint == "" {
		return body
	}
	hint = ansi.Truncate(hint, w, "…")
	hw := ansi.StringWidth(hint)
	i := strings.LastIndexByte(body, '\n') + 1
	row := ansi.Truncate(body[i:], max(w-hw-1, 0), "")
	row += "\x1b[m" + strings.Repeat(" ", max(w-hw-ansi.StringWidth(row), 0))
	return body[:i] + row + hint
}

// runningLabel 描述正在做的事：生成、思考、执行哪个工具，以及已耗时。
func (m *Model) runningLabel() string {
	label := "生成中"
	switch names := m.runningTools(); {
	case len(names) == 1:
		label = "执行 " + names[0]
	case len(names) > 1:
		label = fmt.Sprintf("并行执行 %d 个工具", len(names))
	case m.think != nil:
		label = "思考中"
	}
	// 超过几秒才显示耗时，短回答不闪烁。
	if !m.started.IsZero() {
		if d := time.Since(m.started); d >= 3*time.Second {
			label += fmt.Sprintf(" %ds", int(d.Seconds()))
		}
	}
	return m.sp.View() + " " + label
}

// scrollHint 在用户上滚查看历史时提示当前位置和回到底部的方法。
func (m *Model) scrollHint() string {
	if m.follow {
		return ""
	}
	s := fmt.Sprintf("历史 %d%%，End 回到底部", int(m.vp.ScrollPercent()*100))
	if m.unseen {
		s += "，有新内容"
	}
	return styWarn.Render(s)
}
