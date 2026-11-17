package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// 状态栏里的每一项。项目名是 statusline.jsonc 里写的字符串，也是用户可见接口，不要改名。
const (
	statusPath      = "路径"
	statusTurns     = "对话轮次"
	statusElapsed   = "耗时"
	statusMem       = "内存"
	statusCPU       = "CPU"
	statusInput     = "输入"
	statusOutput    = "输出"
	statusCache     = "缓存"
	statusCost      = "花费"
	statusContext   = "上下文"
	statusModel     = "模型"
	statusEffort    = "思考强度"
	statusState     = "状态"
	statusSpeed     = "速度"
	statusCompact   = "压缩"
	statusMCP       = "MCP"
	statusPlugins   = "插件"
	statusSkills    = "技能"
	statusTools     = "工具"
	statusGit       = "Git"
	maxStatusLine   = 3
	statusItemOrder = "路径 对话轮次 耗时 内存 CPU 输入 输出 缓存 花费 上下文 模型 思考强度 状态 速度 压缩 MCP 插件 技能 工具 Git"
)

// StatusItem 是状态栏的一项。Line 为 0 表示用内置行号，Color 为空表示用内置颜色。
type StatusItem struct {
	Name    string
	Line    int
	Color   string
	Enabled bool
}

// DefaultStatusItems 是内置布局，也用来补全配置文件里没写的项。
func DefaultStatusItems() []StatusItem {
	items := make([]StatusItem, 0, len(statusDefs))
	for _, name := range statusNames() {
		d := statusDefs[name]
		items = append(items, StatusItem{Name: name, Line: d.line, Color: d.color, Enabled: true})
	}
	return items
}

// StatusItemNames 返回全部项目名，按内置顺序排版。给帮助文本和报错用。
func StatusItemNames() []string { return statusNames() }

func statusNames() []string { return strings.Fields(statusItemOrder) }

// ResolveStatusline 把用户配置合并到内置布局上：没写的项不显示，写了但没指定行号 / 颜色的沿用内置值。
// enabled 为 false 的项直接丢掉——它对用户和“没写”是同一个意思。
func ResolveStatusline(items []StatusItem) []StatusItem {
	if len(items) == 0 {
		return DefaultStatusItems()
	}
	out := make([]StatusItem, 0, len(items))
	for _, it := range items {
		d, ok := statusDefs[it.Name]
		if !ok || !it.Enabled {
			continue
		}
		if it.Line == 0 {
			it.Line = d.line
		}
		if it.Color == "" {
			it.Color = d.color
		}
		out = append(out, it)
	}
	return out
}

// colorRe 认三种写法：256 色编号（0-255）、#RRGGBB / #RGB、ANSI 颜色名（red、bright-cyan 这类）。
var (
	colorNumRe  = regexp.MustCompile(`^\d{1,3}$`)
	colorHexRe  = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)
	colorNameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z-]*$`)
)

// ValidateStatusline 检查配置里的各项。返回的问题每条一行，调用方决定是报错退出还是只提示。
func ValidateStatusline(items []StatusItem) []string {
	var probs []string
	seen := map[string]bool{}
	for i, it := range items {
		name := strings.TrimSpace(it.Name)
		if name == "" {
			probs = append(probs, fmt.Sprintf("第 %d 项没有 name", i+1))
			continue
		}
		if _, ok := statusDefs[name]; !ok {
			probs = append(probs, fmt.Sprintf("第 %d 项 %q 不是可用的项目名；可用的是：%s", i+1, name, strings.Join(statusNames(), "、")))
		}
		if seen[name] {
			probs = append(probs, fmt.Sprintf("%q 出现了不止一次", name))
		}
		seen[name] = true
		if it.Line != 0 && (it.Line < 1 || it.Line > maxStatusLine) {
			probs = append(probs, fmt.Sprintf("%q 的 line 是 %d，只能写 1-%d", name, it.Line, maxStatusLine))
		}
		if c := strings.TrimSpace(it.Color); c != "" && !validStatusColor(c) {
			probs = append(probs, fmt.Sprintf("%q 的 color %q 无法识别，请写 0-255 的编号、#RRGGBB 或颜色名", name, it.Color))
		}
	}
	return probs
}

func validStatusColor(c string) bool {
	if colorNumRe.MatchString(c) {
		n, err := strconv.Atoi(c)
		return err == nil && n >= 0 && n <= 255
	}
	return colorHexRe.MatchString(c) || colorNameRe.MatchString(c)
}

// statusDef 是一项的内置定义：默认行号、默认颜色和取值方式。
type statusDef struct {
	line  int
	color string
	// value 返回这一项要显示的值；ok 为 false 表示当前没有数据（还没采样、没回答过、不在仓库里），整项不显示。
	value func(*Model) (seg, bool)
}

// statusDefs 是全部项目及其内置行号、颜色。键必须与 statusItemOrder 里的一致，测试保证两边不漏。
//
// 颜色每项一种、互不重复：标签和值同色，一眼就能看出哪个值属于哪一项。
// 同一行相邻的几项刻意拉开色相，也不用 0-15 号基础色——基础色跟随终端主题，换主题可能变成同一种颜色。
var statusDefs = map[string]statusDef{
	statusPath:    {1, "75", func(m *Model) (seg, bool) { return seg{label: statusPath, value: m.opts.Info.Cwd, flex: true}, true }},
	statusTurns:   {1, "208", func(m *Model) (seg, bool) { return seg{label: statusTurns, value: fmt.Sprint(m.turns())}, true }},
	statusElapsed: {1, "220", func(m *Model) (seg, bool) { return seg{label: statusElapsed, value: m.elapsedValue()}, true }},
	statusMem:     {1, "114", func(m *Model) (seg, bool) { return m.memSeg() }},
	statusCPU:     {1, "176", func(m *Model) (seg, bool) { return m.cpuSeg() }},

	statusInput:   {2, "80", func(m *Model) (seg, bool) { return seg{label: statusInput, value: fmtTokens(m.usage.Input)}, true }},
	statusOutput:  {2, "211", func(m *Model) (seg, bool) { return seg{label: statusOutput, value: fmtTokens(m.usage.Output)}, true }},
	statusCache:   {2, "179", func(m *Model) (seg, bool) { return seg{label: statusCache, value: m.cacheValue()}, true }},
	statusCost:    {2, "120", func(m *Model) (seg, bool) { return seg{label: statusCost, value: m.cost.String()}, true }},
	statusContext: {2, "111", func(m *Model) (seg, bool) { return m.contextSeg(), true }},
	statusModel:   {2, "141", func(m *Model) (seg, bool) { return seg{label: statusModel, value: m.opts.Info.Model}, true }},
	statusEffort: {2, "152", func(m *Model) (seg, bool) {
		e := m.opts.Info.Effort
		return seg{label: statusEffort, value: e}, e != ""
	}},

	statusState:   {3, "250", func(m *Model) (seg, bool) { return seg{label: statusState, value: m.statusName()}, true }},
	statusSpeed:   {3, "159", func(m *Model) (seg, bool) { return seg{label: statusSpeed, value: m.speedValue()}, true }},
	statusCompact: {3, "117", func(m *Model) (seg, bool) { return seg{label: statusCompact, value: fmt.Sprint(m.compactions)}, true }},
	statusMCP:     {3, "37", func(m *Model) (seg, bool) { return seg{label: statusMCP, value: m.mcpSegValue()}, true }},
	statusPlugins: {3, "168", func(m *Model) (seg, bool) { return seg{label: statusPlugins, value: placeholder}, true }},
	statusSkills:  {3, "105", func(m *Model) (seg, bool) { return seg{label: statusSkills, value: m.skillsSegValue()}, true }},
	statusTools: {3, "215", func(m *Model) (seg, bool) {
		return seg{label: statusTools, value: fmt.Sprint(len(m.opts.Info.Tools))}, true
	}},
	statusGit: {3, "209", func(m *Model) (seg, bool) { return seg{label: statusGit, value: fmtGit(m.git)}, m.git.OK }},
}

// memSeg 是“内存”一项：整机占用百分比，后面跟上 sprout 自己占的 MB（读不到时只写百分比）。
func (m *Model) memSeg() (seg, bool) {
	r := m.res
	if r.Mem < 0 {
		return seg{}, false
	}
	v := fmt.Sprintf("%.0f%%", r.Mem)
	if r.ProcMB > 0 {
		v += " · " + fmtMB(r.ProcMB)
	}
	return seg{label: statusMem, value: v}, true
}

// cpuSeg 是“CPU”一项：整机占用，后面跟系统负载（macOS、Android 读不到，只写百分比）。
func (m *Model) cpuSeg() (seg, bool) {
	r := m.res
	if r.CPU < 0 {
		return seg{}, false
	}
	v := fmt.Sprintf("%.0f%%", r.CPU)
	if r.Load >= 0 {
		v += fmt.Sprintf(" · 负载 %.2f", r.Load)
	}
	return seg{label: statusCPU, value: v}, true
}

// elapsedValue 是“耗时”一项：本会话累计的干活时间，后面跟上一次调用的首字延迟。
// 还没干过活时写占位符，而不是 0s。
func (m *Model) elapsedValue() string {
	if m.busyTotal == 0 && m.busySince.IsZero() && m.ttft == 0 {
		return placeholder
	}
	v := fmtElapsed(m.elapsed())
	if m.ttft > 0 {
		v += " · 首字 " + fmtSeconds(m.ttft)
	}
	return v
}

// speedValue 是“速度”一项：最近一次模型调用的输出速度；还没有可算的数据时写占位符，单位照写。
func (m *Model) speedValue() string {
	if m.speed <= 0 {
		return placeholder + " token/s"
	}
	return fmt.Sprintf("%.1f token/s", m.speed)
}

// cacheValue 是“缓存”一项：读 / 写 token 数，后面跟缓存命中率。一次用量都没有时只写 0/0，
// 不写 0.0%——那会被当成“命中率很低”，而实际情况是还没有数据。
func (m *Model) cacheValue() string {
	u := m.usage
	v := fmtTokens(u.CacheRead) + "/" + fmtTokens(u.CacheWrite)
	if total := u.Input + u.CacheRead + u.CacheWrite; total > 0 {
		v += fmt.Sprintf(" · %.1f%%", float64(u.CacheRead)*100/float64(total))
	}
	return v
}
