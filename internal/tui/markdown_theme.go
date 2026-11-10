package tui

import "charm.land/lipgloss/v2"

// Markdown 配色。全部使用 ANSI 基础色并跟随终端主题，不设背景色：
// 没有可靠的深浅色背景探测时，固定的背景块在另一种主题下会看不清。
var (
	mdFaint = lipgloss.NewStyle().Faint(true)
	mdBold  = lipgloss.NewStyle().Bold(true)

	mdH1 = lipgloss.NewStyle().Bold(true).Underline(true).Foreground(lipgloss.Color("5"))
	mdH2 = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("5"))
	mdH3 = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3")) // 三级及以下

	mdCodeSpan = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	mdLink     = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Underline(true)
	mdMarker   = lipgloss.NewStyle().Foreground(lipgloss.Color("6")) // 列表圆点与序号
	mdQuoteBar = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	mdCheckOn  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))

	// 代码块里没有语法高亮可用时（没写语言、语言不认识）的统一颜色。
	mdCodePlain = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
)

func mdHeadingStyle(level int) lipgloss.Style {
	switch level {
	case 1:
		return mdH1
	case 2:
		return mdH2
	}
	return mdH3
}
