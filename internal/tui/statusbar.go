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
// MCP、插件、技能对应的功能还没有实现，先占位显示 placeholder，接上之后只需要把值换掉。
// 花费已接上：按 models.dev 的单价累计，见 costTotals。

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

// costTotals 是本会话的累计花费。每次用量到达时按当时模型的单价累加，
// 中途切换模型后，之前的部分仍按旧模型的价格算，不会被新价格重算。
type costTotals struct {
	USD     float64
	priced  bool // 至少有一次用量带价格
	partial bool // 至少有一次用量没有价格，USD 只是下限
}

// add 按单价累加一次用量的花费；单价是美元 / 百万 token。
func (c *costTotals) add(p Price, input, output, cacheRead, cacheWrite int) {
	if !p.Known {
		c.partial = true
		return
	}
	c.priced = true
	c.USD += (float64(input)*p.Input + float64(output)*p.Output +
		float64(cacheRead)*p.CacheRead + float64(cacheWrite)*p.CacheWrite) / 1e6
}

// String 是“花费”一项的值：没有任何价格时是占位符；有一部分用量没价格时前面加 ≥，表示只是下限。
func (c costTotals) String() string {
	if !c.priced {
		return placeholder
	}
	s := fmtUSD(c.USD)
	if c.partial {
		s = "≥" + s
	}
	return s
}

// fmtUSD：不到 1 美元保留四位小数，否则两位。单轮花费常常只有零点几美分，两位小数会一直显示 $0.00。
func fmtUSD(v float64) string {
	if v < 1 {
		return fmt.Sprintf("$%.4f", v)
	}
	return fmt.Sprintf("$%.2f", v)
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

// labelColors 是各项的颜色（256 色编号），每项一种，互不重复；标签和值用同一种颜色，一眼就能看出哪个值属于哪一项。
// statusbar_test 保证不重复、不漏登记。唯一的例外是上下文接近上限时值变黄或变红，那是提醒，不是装饰。
//
// 同一行相邻的几项刻意拉开色相，不用 0-15 号基础色：基础色跟随终端主题，换个主题可能变成同一种颜色。
var labelColors = map[string]string{
	// 第一行
	"路径":   "75",  // 天蓝
	"对话轮次": "208", // 橙
	"耗时":   "220", // 金黄
	"内存":   "114", // 浅绿
	"CPU":  "176", // 粉紫
	// 第二行
	"输入":   "80",  // 青
	"输出":   "211", // 粉
	"缓存":   "179", // 土黄
	"花费":   "120", // 亮绿
	"上下文":  "111", // 浅蓝
	"模型ID": "141", // 紫
	"思考强度": "152", // 浅青灰
	// 第三行
	"状态":  "250", // 灰
	"MCP": "37",  // 深青
	"插件":  "168", // 玫红
	"技能":  "105", // 蓝紫
	"工具":  "215", // 杏色
}

// labelStyle 返回一项的样式；没登记的标签用淡色，不会因为漏登记而崩溃。
func labelStyle(label string) lipgloss.Style {
	if c, ok := labelColors[label]; ok {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(c))
	}
	return styFaint
}

// seg 是状态栏里的一项：标签 + 值，两者同色（见 labelColors）。
type seg struct {
	label string
	value string          // 纯文本，不带样式
	alert *lipgloss.Style // 非空时值改用这个颜色（上下文快满的提醒）
}

func (s seg) render() string {
	st := labelStyle(s.label)
	if s.alert != nil {
		return st.Render(s.label) + " " + s.alert.Render(s.value)
	}
	return st.Render(s.label + " " + s.value)
}

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

// elapsed 是本会话里 agent 实际干活的累计时长：每轮回答和 /compact 从开始到结束的时间相加，
// 空闲（等用户输入）时不涨。恢复的会话不知道当初花了多久，从 0 开始。
func (m *Model) elapsed() time.Duration {
	d := m.busyTotal
	if !m.busySince.IsZero() {
		d += m.now().Sub(m.busySince)
	}
	return max(d, 0)
}

// busyStart、busyStop 标记一段干活时间的起止；只在一轮回答或一次压缩的开头和结尾调用。
func (m *Model) busyStart() { m.busySince = m.now() }

func (m *Model) busyStop() {
	if !m.busySince.IsZero() {
		m.busyTotal += max(m.now().Sub(m.busySince), 0) // 时钟回拨时不倒扣
		m.busySince = time.Time{}
	}
}

// resetBusy 在新会话、恢复会话时把耗时清零。
func (m *Model) resetBusy() { m.busyTotal, m.busySince = 0, time.Time{} }

// infoPathLine：路径、对话轮次、耗时、内存、CPU。路径是唯一可伸缩的一项，把剩下的宽度都给它，
// 放不下时从左边截断（保留末尾几级最有辨识度的部分），其他项宁可整行被截也不为它让位。
func (m *Model) infoPathLine() string {
	rest := []seg{
		{"对话轮次", fmt.Sprint(m.turns()), nil},
		{"耗时", fmtElapsed(m.elapsed()), nil},
	}
	if r := m.res; r.Mem >= 0 {
		rest = append(rest, seg{"内存", fmt.Sprintf("%.0f%%", r.Mem), nil})
	}
	if r := m.res; r.CPU >= 0 {
		rest = append(rest, seg{"CPU", fmt.Sprintf("%.0f%%", r.CPU), nil})
	}
	fixed := 0
	for _, s := range rest {
		fixed += ansi.StringWidth(s.render()) + len(segSep)
	}
	room := max(m.w-fixed-ansi.StringWidth("路径 "), 8)
	path := seg{"路径", truncateLeft(m.opts.Info.Cwd, room), nil}
	return joinSegs(m.w, append([]seg{path}, rest...))
}

// infoUsageLine：本会话累计的输入输出、缓存、花费、上下文占用、模型和思考强度。
func (m *Model) infoUsageLine() string {
	u := m.usage
	segs := []seg{
		{"输入", fmtTokens(u.Input), nil},
		{"输出", fmtTokens(u.Output), nil},
		{"缓存", fmtTokens(u.CacheRead) + "/" + fmtTokens(u.CacheWrite), nil},
		{"花费", m.cost.String(), nil},
		m.contextSeg(),
		{"模型ID", m.opts.Info.Model, nil},
	}
	if e := m.opts.Info.Effort; e != "" {
		segs = append(segs, seg{"思考强度", e, nil})
	}
	return joinSegs(m.w, segs)
}

// contextSeg 是“上下文”一项：知道窗口大小时是 占用%/窗口，不知道时只写当前占用的 token 数。
// 占用接近压缩的触发线时值变黄，快满了变红。
func (m *Model) contextSeg() seg {
	s := seg{label: "上下文"}
	win := m.opts.Info.ContextWindow
	if win <= 0 {
		s.value = placeholder
		if m.ctxTokens > 0 {
			s.value = fmtTokens(m.ctxTokens)
		}
		return s
	}
	pct := float64(m.ctxTokens) * 100 / float64(win)
	s.value = fmt.Sprintf("%.1f%%/%s", pct, fmtTokens(win))
	switch {
	case pct >= 90:
		s.alert = &styErr
	case pct >= 70:
		s.alert = &styWarn
	}
	return s
}

// infoStatusLine：当前在做什么，以及尚未实现的 MCP、插件、技能，和可用的工具数。
func (m *Model) infoStatusLine() string {
	return joinSegs(m.w, []seg{
		{"状态", m.statusName(), nil},
		{"MCP", placeholder, nil},
		{"插件", placeholder, nil},
		{"技能", placeholder, nil},
		{"工具", fmt.Sprint(len(m.opts.Info.Tools)), nil},
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
