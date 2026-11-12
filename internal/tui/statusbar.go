package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// 底部三行状态栏，从上到下：
//
//	路径 … | 对话轮次 N | 耗时 … | 内存 N% | CPU N%
//	输入 … | 输出 … | 缓存 读/写 | 花费 — | 上下文 N%/窗口 | 模型ID … | 思考强度 …
//	状态 … | MCP — | 插件 — | 技能 — | 工具 N
//
// MCP、插件、技能、花费对应的功能还没有实现，先占位显示 placeholder，接上之后只需要把值换掉。

// infoRows 是状态栏占的行数。
const infoRows = 3

// placeholder 是尚未实现的功能在状态栏里显示的值。
const placeholder = "—"

// Resources 是整机的内存和 CPU 占用百分比（0-100）；负数表示读不到（平台不支持、没有权限，
// 或者 CPU 还没有第二次采样可比），状态栏就不显示那一项。
type Resources struct {
	Mem, CPU float64
}

// usageTotals 是本会话累计的 token 用量，按服务端报告的数字累加。
type usageTotals struct {
	Input      int // 未命中缓存的输入（总输入减去缓存读写），与“缓存”一项加起来才是全部输入
	Output     int
	CacheRead  int
	CacheWrite int
}

// resourceMsg 携带一次采样结果。收到后再约定一秒后的下一次采样，同时也让耗时每秒刷新。
type resourceMsg struct{ res Resources }

// resourceCmd 在 delay 之后采样一次。采样在命令自己的 goroutine 里做，不占用 Update。
// 没有提供 Resources 时只返回两个“读不到”，状态栏仍然每秒刷新耗时。
func (m *Model) resourceCmd(delay time.Duration) tea.Cmd {
	sample := m.opts.Resources
	run := func() tea.Msg {
		if sample == nil {
			return resourceMsg{Resources{Mem: -1, CPU: -1}}
		}
		return resourceMsg{sample()}
	}
	if delay <= 0 {
		return func() tea.Msg { return run() }
	}
	return tea.Tick(delay, func(time.Time) tea.Msg { return run() })
}

// 各项标签的颜色。值用默认前景色，只有需要提醒的（上下文接近上限）才换色。
var (
	styLblPath    = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	styLblTurns   = lipgloss.NewStyle().Foreground(lipgloss.Color("208"))
	styLblElapsed = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styLblMem     = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styLblCPU     = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	styLblInput   = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	styLblOutput  = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))
	styLblCache   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	styLblCost    = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styLblCtx     = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	styLblModel   = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styLblEffort  = lipgloss.NewStyle().Faint(true)
	styLblStatus  = lipgloss.NewStyle().Faint(true)
	styLblMCP     = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	styLblPlugin  = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	styLblSkill   = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	styLblTool    = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
)

// seg 是状态栏里的一项：标签 + 值。
type seg struct {
	label string
	lbl   lipgloss.Style
	value string // 已经渲染好的值（可以带颜色）
}

func (s seg) render() string { return s.lbl.Render(s.label) + " " + s.value }

const segSep = " | "

// joinSegs 把各项用淡色竖线连起来，整行截到 width 以内。
func joinSegs(width int, segs []seg) string {
	parts := make([]string, len(segs))
	for i, s := range segs {
		parts[i] = s.render()
	}
	return ansi.Truncate(strings.Join(parts, styFaint.Render(segSep)), width, "…")
}

// infoLines 返回底部状态栏的三行。
func (m *Model) infoLines() []string {
	return []string{m.infoPathLine(), m.infoUsageLine(), m.infoStatusLine()}
}

// fmtElapsed 把时长写成 46m43s、1h02m03s；不到一分钟只写秒。
func fmtElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d.Seconds())
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh%02dm%02ds", s/3600, s%3600/60, s%60)
}

func (m *Model) turns() int {
	n := 0
	for _, e := range m.entries {
		if e.kind == kindUser {
			n++
		}
	}
	return n
}

func (m *Model) elapsed() time.Duration {
	if m.sessionStart.IsZero() {
		return 0
	}
	return m.now().Sub(m.sessionStart)
}

// infoPathLine：路径、对话轮次、耗时、内存、CPU。路径是唯一可伸缩的一项，把剩下的宽度都给它，
// 放不下时从左边截断（保留末尾几级最有辨识度的部分），其他项宁可整行被截也不为它让位。
func (m *Model) infoPathLine() string {
	rest := []seg{
		{"对话轮次", styLblTurns, fmt.Sprint(m.turns())},
		{"耗时", styLblElapsed, fmtElapsed(m.elapsed())},
	}
	if r := m.res; r.Mem >= 0 {
		rest = append(rest, seg{"内存", styLblMem, fmt.Sprintf("%.0f%%", r.Mem)})
	}
	if r := m.res; r.CPU >= 0 {
		rest = append(rest, seg{"CPU", styLblCPU, fmt.Sprintf("%.0f%%", r.CPU)})
	}
	fixed := 0
	for _, s := range rest {
		fixed += ansi.StringWidth(s.render()) + len(segSep)
	}
	room := max(m.w-fixed-ansi.StringWidth("路径 "), 8)
	path := seg{"路径", styLblPath, styInfoDir.Render(truncateLeft(m.opts.Info.Cwd, room))}
	return joinSegs(m.w, append([]seg{path}, rest...))
}

// infoUsageLine：本会话累计的输入输出、缓存、花费、上下文占用、模型和思考强度。
func (m *Model) infoUsageLine() string {
	u := m.usage
	segs := []seg{
		{"输入", styLblInput, fmtTokens(u.Input)},
		{"输出", styLblOutput, fmtTokens(u.Output)},
		{"缓存", styLblCache, fmtTokens(u.CacheRead) + "/" + fmtTokens(u.CacheWrite)},
		{"花费", styLblCost, placeholder},
		{"上下文", styLblCtx, m.contextValue()},
		{"模型ID", styLblModel, styInfoModel.Render(m.opts.Info.Model)},
	}
	if e := m.opts.Info.Effort; e != "" {
		segs = append(segs, seg{"思考强度", styLblEffort, styInfoThink.Render(e)})
	}
	return joinSegs(m.w, segs)
}

// contextValue 是“上下文”一项的值：知道窗口大小时是 占用%/窗口，不知道时只写当前占用的 token 数。
// 占用接近压缩的触发线时变黄，快满了变红。
func (m *Model) contextValue() string {
	win := m.opts.Info.ContextWindow
	if win <= 0 {
		if m.ctxTokens <= 0 {
			return placeholder
		}
		return fmtTokens(m.ctxTokens)
	}
	pct := float64(m.ctxTokens) * 100 / float64(win)
	text := fmt.Sprintf("%.1f%%/%s", pct, fmtTokens(win))
	switch {
	case pct >= 90:
		return styErr.Render(text)
	case pct >= 70:
		return styWarn.Render(text)
	}
	return text
}

// infoStatusLine：当前在做什么，以及尚未实现的 MCP、插件、技能，和可用的工具数。
func (m *Model) infoStatusLine() string {
	return joinSegs(m.w, []seg{
		{"状态", styLblStatus, m.statusName()},
		{"MCP", styLblMCP, placeholder},
		{"插件", styLblPlugin, placeholder},
		{"技能", styLblSkill, placeholder},
		{"工具", styLblTool, fmt.Sprint(len(m.opts.Info.Tools))},
	})
}

// statusName 是“状态”一项的值。
func (m *Model) statusName() string {
	switch {
	case m.resuming:
		return "恢复会话"
	case m.switching:
		return "切换模型"
	}
	if label := m.busyLabel(); label != "" {
		return label
	}
	return "空闲"
}

// busyLabel 描述正在做的事；空闲时返回空串。活动行和状态栏用的是同一套说法。
func (m *Model) busyLabel() string {
	names := m.runningTools()
	switch {
	case m.compacting:
		return "压缩上下文"
	case len(names) == 1:
		return "执行 " + names[0]
	case len(names) > 1:
		return fmt.Sprintf("并行执行 %d 个工具", len(names))
	case m.think != nil:
		return "思考中"
	case m.running:
		return "生成中"
	}
	return ""
}
