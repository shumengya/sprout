package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// View 渲染整个界面：transcript、分隔线、状态行、输入框、信息栏。
// 模型/目录/工具这类不常变的信息放在最下面一行，顶部整块留给对话。
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
	v.Content = m.body() + "\n" + sep + "\n" + m.statusLine() + "\n" + m.in.View() + "\n" + m.infoLine()

	// 把输入框的光标位置换算到整个界面里：它在对话区、分隔线、状态行之后。
	if c := m.in.Cursor(); c != nil {
		c.Y += m.vp.Height() + 2
		v.Cursor = c
	}
	return v
}

var (
	styInfoLabel = lipgloss.NewStyle().Faint(true)
	styInfoModel = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))  // 绿
	styInfoDir   = lipgloss.NewStyle().Foreground(lipgloss.Color("12")) // 亮蓝：普通蓝在深色背景上太暗
	styInfoTool  = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))  // 洋红，和工具卡片同色系
)

// infoLine 是最底下的信息栏：模型、当前目录、可用工具，用不同颜色区分。
// 宽度不够时目录最先让步（从左边截断，保留末尾几级最有辨识度的部分），其次才是整行截断。
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

	return ansi.Truncate(model+gap+dirPart+gap+toolPart, m.w, "…")
}

// truncateLeft 把 s 截到不超过 w 列，超出时从左边去掉并以 … 开头。
func truncateLeft(s string, w int) string {
	if ansi.StringWidth(s) <= w {
		return s
	}
	return ansi.TruncateLeft(s, ansi.StringWidth(s)-(w-1), "…")
}

func (m *Model) statusLine() string {
	left := m.activity()
	if hint := m.scrollHint(); hint != "" {
		left += "  " + hint
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

// activity 是状态行左侧：当前在做什么。
func (m *Model) activity() string {
	switch {
	case m.status != "" && m.statusOK:
		return styOK.Render(m.status)
	case m.status != "":
		return styWarn.Render(m.status)
	case !m.running:
		return styFaint.Render("空闲")
	}
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
