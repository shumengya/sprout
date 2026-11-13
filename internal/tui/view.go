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
	input, overlaid := m.overlay() // 选择器或档位条替换输入框，对话区不被遮挡
	if !overlaid {
		input = m.inputView()
	}
	// 活动行存在时，历史提示叠在活动行右端，与左侧提示同一行；否则仍叠在对话区最后一行。
	// 两者都在时叠到对话区会让提示看起来错位一行。
	hint, act := m.scrollHint(), m.activityLine()
	if act != "" && hint != "" {
		act, hint = overlayHint(act, hint, m.w), ""
	}
	parts := []string{overlayHint(m.body(), hint, m.w)}
	above := m.vp.Height() // 输入框上面的行数：对话区、活动行（如果有）、分隔线
	if act != "" {
		parts = append(parts, act)
		above++
	}
	if !overlaid && len(m.sug.items) > 0 {
		sv := m.suggestView(m.w)
		parts = append(parts, sv)
		above += strings.Count(sv, "\n") + 1
	}
	parts = append(parts, sep, input, sep)
	parts = append(parts, m.infoLines()...) // 状态栏在输入框下方，不影响光标坐标
	above++
	v.Content = strings.Join(parts, "\n")

	// 把输入框的光标位置换算到整个界面里。选择器打开时没有输入光标。
	if !overlaid {
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
	styInfoThink = lipgloss.NewStyle().Foreground(lipgloss.Color("14")) // 亮青：与模型名区分
	styEffortSel = lipgloss.NewStyle().Bold(true).Reverse(true)
)

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
	case m.running || m.compacting:
		left = m.runningLabel()
	default:
		return ""
	}
	if !m.running && !m.compacting {
		return ansi.Truncate(left, m.w, "…")
	}
	right := styFaint.Render("Esc 取消")
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

// runningLabel 描述正在做的事：生成、思考、执行哪个工具，以及已耗时。文字与状态栏的“状态”一项共用 busyLabel。
func (m *Model) runningLabel() string {
	label := m.busyLabel()
	if label == "" {
		label = "生成中"
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
