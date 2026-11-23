package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// 底部状态栏，默认三行：
//
//	路径 … | 对话轮次 N | 耗时 … | 内存 N% · MB | CPU N% · 负载 N.NN
//	输入 … | 输出 … | 缓存 读/写 · 命中率 | 花费 — | 上下文 N%/窗口 | 模型 … | 思考强度 …
//	状态 … | 速度 N token/s | 压缩 N | MCP … | 插件 — | 技能 … | 工具 N | Git 分支 ↑ahead +新增~修改-删除
//
// 显示哪些项、在第几行、什么颜色由 statusline.jsonc 决定（见 statusline.go）；
// 一项在当前没有数据时整项不显示（还没采样、没回答过、不在 git 仓库里），
// 而“有数据但值为零”照常显示——那两项在配置里看得见差别。
// 耗时和速度是例外：还没有数据时照样占位、值写成 placeholder，免得状态栏随着第一次回答左右跳动。
// 插件还没有实现，固定显示 placeholder。

// infoRows 是默认布局下状态栏占的行数；自定义布局可能更少，实际行数以 infoLines 为准。
const infoRows = 3

// placeholder 是尚未实现的功能在状态栏里显示的值。
const placeholder = "—"

// Resources 是整机的内存和 CPU 占用百分比（0-100）；负数表示读不到（平台不支持、没有权限，
// 或者 CPU 还没有第二次采样可比），状态栏就不显示那一项。
type Resources struct {
	Mem, CPU float64
	// ProcMB 是 sprout 进程自己占用的物理内存（MB）；不大于 0 表示读不到（运行中的进程不可能是 0）。
	ProcMB float64
	// Load 是系统负载（1 分钟平均）；负数表示读不到，状态栏就只显示 CPU 百分比。
	Load float64
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
			return resourceMsg{Resources{Mem: -1, CPU: -1, ProcMB: -1, Load: -1}}
		}
		return resourceMsg{sample()}
	}
	if delay <= 0 {
		return func() tea.Msg { return run() }
	}
	return tea.Tick(delay, func(time.Time) tea.Msg { return run() })
}

// GitInfo 是工作目录的 git 分支和改动文件数。OK 为 false 表示读不到（不是仓库、没装 git），状态栏不显示这一项。
type GitInfo struct {
	Branch                   string
	Added, Modified, Deleted int
	// Ahead、Behind 是相对上游的提交数；没有上游时都是 0。
	Ahead, Behind int
	OK            bool
}

// gitInterval 是 git 状态的采样间隔，比资源采样慢：每次都要起一个 git 进程。
const gitInterval = 5 * time.Second

// gitMsg 携带一次 git 状态的读取结果。
type gitMsg struct{ info GitInfo }

// gitCmd 在 delay 之后读一次 git 状态。读取在命令自己的 goroutine 里做，git 慢也不占 Update；
// 没有提供 Git 时返回 nil，之后也不再调度。
func (m *Model) gitCmd(delay time.Duration) tea.Cmd {
	read := m.opts.Git
	if read == nil {
		return nil
	}
	run := func() tea.Msg { return gitMsg{read()} }
	if delay <= 0 {
		return run
	}
	return tea.Tick(delay, func(time.Time) tea.Msg { return run() })
}

// labelColors 是各项的内置颜色，取自 statusDefs（statusline.go）。配置没写 color 时用它。
// statusbar_test 保证不重复。唯一的例外是上下文接近上限时值变黄或变红，那是提醒，不是装饰。
var labelColors = func() map[string]string {
	m := make(map[string]string, len(statusDefs))
	for name, d := range statusDefs {
		m[name] = d.color
	}
	return m
}()

// labelStyle 返回一项的样式：配置里写了颜色就用它，否则用内置颜色；都没有时用淡色，不会因为漏登记而崩溃。
func labelStyle(label, color string) lipgloss.Style {
	if color != "" {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(color))
	}
	if c, ok := labelColors[label]; ok {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(c))
	}
	return styFaint
}

// seg 是状态栏里的一项：标签 + 值，两者同色。
type seg struct {
	label string
	value string          // 纯文本，不带样式
	color string          // 配置指定的颜色；空表示用内置颜色
	alert *lipgloss.Style // 非空时值改用这个颜色（上下文快满的提醒）
	flex  bool            // 占满剩余宽度（只有路径）
}

func (s seg) render() string {
	st := labelStyle(s.label, s.color)
	if s.alert != nil {
		return st.Render(s.label) + " " + s.alert.Render(s.value)
	}
	return st.Render(s.label + " " + s.value)
}

// segSep 是项与项之间的分隔：淡色的竖线，前后各留一个空格。
const segSep = "│"

// joinSegs 把各项用淡色竖线连起来，整行截到 width 以内。
func joinSegs(width int, segs []seg) string {
	parts := make([]string, len(segs))
	for i, s := range segs {
		parts[i] = s.render()
	}
	return ansi.Truncate(strings.Join(parts, " "+styFaint.Render(segSep)+" "), width, "…")
}

// joinLine 渲染同一行的各项。可伸缩的一项（路径）先按其余各项占掉的位置算出剩余宽度，
// 再从左边截断——保留末尾几级目录，最有辨识度的部分还在。宽度不够时整项让位，宁可截掉也不换行。
func (m *Model) joinLine(segs []seg) string {
	if len(segs) == 0 {
		return ""
	}
	fixed, flex := 0, -1
	for i, s := range segs {
		if s.flex {
			flex = i
			continue
		}
		fixed += ansi.StringWidth(s.render()) + ansi.StringWidth(segSep) + 2 // 分隔线两侧各一个空格
	}
	if flex >= 0 {
		room := max(m.w-fixed-ansi.StringWidth(segs[flex].label+" "), 8)
		segs[flex].value = truncateLeft(segs[flex].value, room)
	}
	return joinSegs(m.w, segs)
}

// infoLines 按 statusline.jsonc 的布局返回底部状态栏。没有内容可显示的行整行省略，
// 所以自定义布局可能比默认的三行少。
func (m *Model) infoLines() []string {
	byLine := make([][]seg, maxStatusLine)
	for _, it := range m.statusline {
		d, ok := statusDefs[it.Name]
		if !ok {
			continue // 配置在加载时已校验过，这里只是防御
		}
		s, ok := d.value(m)
		if !ok {
			continue // 当前没有数据：整项不显示
		}
		s.color = it.Color
		line := it.Line - 1
		if line < 0 || line >= maxStatusLine {
			line = d.line - 1
		}
		byLine[line] = append(byLine[line], s)
	}
	var out []string
	for _, segs := range byLine {
		if line := m.joinLine(segs); line != "" {
			out = append(out, line)
		}
	}
	return out
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

// fmtMB 把 MB 数写成 48MB；超过 1024 换成 GB。
func fmtMB(mb float64) string {
	if mb >= 1024 {
		return fmt.Sprintf("%.1fGB", mb/1024)
	}
	return fmt.Sprintf("%.0fMB", mb)
}

// fmtGit 是“Git”一项的值：分支名、相对上游的领先 / 落后（有上游才写），再是新增（含未跟踪）、修改、删除的文件数。
func fmtGit(g GitInfo) string {
	v := g.Branch
	if g.Ahead > 0 {
		v += fmt.Sprintf(" ↑%d", g.Ahead)
	}
	if g.Behind > 0 {
		v += fmt.Sprintf(" ↓%d", g.Behind)
	}
	return fmt.Sprintf("%s +%d~%d-%d", v, g.Added, g.Modified, g.Deleted)
}

func (m *Model) turns() int {
	n := 0
	for _, e := range m.entries {
		// 还在排队、没发出去的不算一轮；一轮中途接进去的属于那一轮，也不另算。
		if e.kind == kindUser && !e.queued && !e.midTurn {
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

// mcpSegValue 是状态栏“MCP”一项：显示 已连接/已启用 的服务端数。
// 分母只算启用的：在 /mcp 里停用的服务端不该让这一项看起来永远差几个。
// 一个都没启用（或没配置）时才是占位符——启用了但全部连接失败要显示 0/N，那是真实状态。
func (m *Model) mcpSegValue() string {
	enabled, ok, pending := 0, 0, false
	for _, s := range m.opts.Info.MCP {
		if !s.Enabled {
			continue
		}
		enabled++
		switch {
		case s.Pending:
			pending = true
		case s.Err == "":
			ok++
		}
	}
	switch {
	case enabled == 0:
		return placeholder
	case pending:
		return "连接中"
	}
	return fmt.Sprintf("%d/%d", ok, enabled)
}

// skillsSegValue 是状态栏“技能”一项：没有安装技能时显示占位符；
// 有技能在 /skills 里被停用时显示 启用/全部，否则只显示总数。
func (m *Model) skillsSegValue() string {
	n := len(m.opts.Info.Skills)
	if n == 0 {
		return placeholder
	}
	on := 0
	for _, sk := range m.opts.Info.Skills {
		if sk.Enabled {
			on++
		}
	}
	if on == n {
		return fmt.Sprint(n)
	}
	return fmt.Sprintf("%d/%d", on, n)
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
// 有后台任务时，模型给完答案后内核在等它们，说成“等待后台任务”；别的时候在说法后面注明后台还有几个。
func (m *Model) busyLabel() string {
	names := m.runningTools()
	label := ""
	switch {
	case m.compacting:
		label = "压缩上下文"
	case len(names) == 1:
		label = "执行 " + names[0]
	case len(names) > 1:
		label = fmt.Sprintf("并行执行 %d 个工具", len(names))
	case m.think != nil:
		label = "思考中"
	case m.running && m.awaitingBg && m.backgroundTools() > 0:
		return fmt.Sprintf("等待 %d 个后台任务", m.backgroundTools())
	case m.running:
		label = "生成中"
	}
	if n := m.backgroundTools(); label != "" && n > 0 {
		label += fmt.Sprintf(" · 后台 %d 个", n)
	}
	return label
}

// fmtSeconds 把零头很短的时长写成 1.2s、0.4s；不到 10 秒保留一位小数，再长就按 fmtElapsed 的写法。
func fmtSeconds(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < 10*time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmtElapsed(d)
}
