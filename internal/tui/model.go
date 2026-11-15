package tui

import (
	"context"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/event"
)

// Runner 是 TUI 眼中的内核：处理一条输入，过程以事件发给 sink。*agent.Agent 满足它。
type Runner interface {
	Run(ctx context.Context, in agent.Input, sink event.Sink) error
}

// ToolInfo 描述一个可用的终端工具。
type ToolInfo struct {
	Name   string
	Detail string // 如 "PowerShell 7+  C:\Program Files\PowerShell\7\pwsh.exe"
}

// MCPServerInfo 是一个已配置的 MCP 服务端，供状态栏、/tools 与 /mcp 面板展示。
type MCPServerInfo struct {
	Name    string
	URL     string // 远程服务端的地址；stdio 服务端为空
	Command string // stdio 服务端的启动命令；远程服务端为空
	Version string // 协商出的协议版本；连接失败或还没连上时为空
	Server  string // 服务端自报的名字；可以没有
	// Enabled 是 mcp.jsonc 里的开关。没启用的服务端不会去连接，Tools 为空。
	Enabled bool
	// Tools 是服务端提供的工具；没连接上时为空。TUI 只看 Enabled，不关心 excludeTools 的原始形态。
	Tools []MCPToolInfo
	Err   string // 非空表示连接失败
	// Pending 表示还在连接中。启动时连接在后台进行，界面先显示这一状态。
	Pending bool
}

// ToolCount 是服务端提供且未被屏蔽的工具数。
func (s MCPServerInfo) ToolCount() int {
	n := 0
	for _, t := range s.Tools {
		if t.Enabled {
			n++
		}
	}
	return n
}

// SkillInfo 是一个已安装的技能。Path 是 SKILL.md 的绝对路径，用来认出模型读到的是哪个技能。
type SkillInfo struct {
	Name    string
	Path    string
	Desc    string // SKILL.md 里的 description，面板里截断显示
	Source  string // 项目 / 用户 / 自定义
	Enabled bool   // 是否把它的元数据交给模型
}

// Info 是 header 与 /tools 展示的信息，由 main 装配好传入。
type Info struct {
	Model       string
	Provider    string
	Cwd         string
	DataDir     string
	Tools       []ToolInfo // 实际可用的工具
	Unavailable []string   // 不可用的工具及原因，每项一行
	// MCP 是已配置的 MCP 服务端；没有配置任何服务端时为空，状态栏显示占位符。
	MCP []MCPServerInfo
	// Skills 是已安装的技能。为空表示没有安装技能，状态栏显示占位符。
	Skills []SkillInfo
	// Vision 表示当前模型支持图片输入；为 false 时仍可附加图片，但会提示会被省略。
	Vision bool
	// Effort 是当前思考档位；为空表示当前模型没有声明 effort，信息栏不显示。
	Effort string
	// ContextWindow 是当前模型的上下文窗口（token）；0 表示未知，状态栏只显示当前占用的 token 数。
	ContextWindow int
	// Price 是当前模型的单价（美元 / 百万 token）。Known 为 false 时状态栏的“花费”显示占位符。
	Price Price
}

// Price 是一个模型各项 token 的单价，单位美元 / 百万 token。
// 单独定义而不是复用 config.ModelPrice：tui 不能 import 内核的配置包。
type Price struct {
	Input      float64 // 没命中缓存的输入
	Output     float64
	CacheRead  float64
	CacheWrite float64
	Known      bool // 有价格数据；缓存价为 0 是正常的（该模型不计缓存费），不能拿来判断有没有价格
}

// Options 是构造 Model 所需的全部输入。
type Options struct {
	Runner Runner
	Info   Info
	// Disabled 非空表示当前不能发起对话（如缺少 API Key），提交时不会调用 Runner。
	Disabled string
	// Notices 是启动时显示在 transcript 顶部的提示。
	Notices []string
	// Sessions 提供会话列表与恢复；为 nil 时 /resume 不可用。
	Sessions Sessions
	// Replay 是启动时就恢复了会话（--continue）时要重放的事件流，ResumeNotice 是对应的说明。
	Replay       []event.Event
	ResumeNotice string
	// Installer 注册和卸载全局 sprout 命令（/install-sprout、/uninstall-sprout）；为 nil 时这两个命令不可用。
	Installer Installer
	// Models 提供模型列表与切换（/model）；为 nil 时 /model 不可用。
	Models Models
	// Efforts 调整思考程度（/effort）；为 nil 时 /effort 不可用。
	Efforts Efforts
	// Compactor 手动压缩上下文（/compact）；为 nil 时 /compact 不可用。
	Compactor Compactor
	// Resources 采样整机内存和 CPU 占用，状态栏每秒调用一次（在 tea.Cmd 里，不占 Update）；
	// 为 nil 时状态栏不显示这两项。
	Resources func() Resources
	// Git 读取工作目录的 git 分支和改动文件数，状态栏每几秒调用一次（在 tea.Cmd 里，不占 Update）；
	// 为 nil 或返回 OK=false 时不显示 Git 一项。
	Git func() GitInfo
	// MCPReady 在后台的 MCP 连接流程结束时关闭；为 nil 表示没有后台连接（启动时已就绪或没有配置）。
	MCPReady <-chan struct{}
	// ApplyMCP 在 MCP 连接完成后调用，返回刷新后的 Info 与要显示的提示（连接失败等）。
	// 它会重建 agent 并保留对话历史，因此只能在空闲时调用；TUI 会等到一轮结束再调。
	ApplyMCP func() (Info, []string)
	// MCP 提供 /mcp 面板的读写操作；为 nil 时 /mcp 不可用。
	MCP MCPPanel
	// Skills 提供 /skills 面板的读写操作；为 nil 时 /skills 不可用。
	Skills SkillPanel
}

// 输入框按内容增高：空时一行，最多显示这么多行，再多就在输入框内部滚动。
const inputMaxRows = 8

// Model 是 Bubble Tea 的主模型，子组件不各自跑 Update。
type Model struct {
	opts Options
	w, h int

	vp viewport.Model
	in textarea.Model
	sp spinner.Model

	entries []*entry
	tools   map[string]*entry // tool call id → 条目
	cur     *entry            // 正在流式追加的 assistant 条目
	think   *entry            // 正在流式追加的思维链条目
	// thinkShown：本次模型调用是否已经流式展示过思维链，用来避免 MessageCompleted 时重复补一个。
	thinkShown bool
	// interrupted：本轮已经由 interrupted 事件说明过中断原因，结束时不再重复一条“已取消”。
	interrupted bool
	follow      bool // transcript 是否自动跟随底部
	unseen      bool // 暂停跟随期间是否有新内容到达

	showThink bool            // 是否展开思维链全文（/think 切换）
	mouse     bool            // 是否开启鼠标上报（/mouse 切换）；开启后滚轮可滚动，但终端自带的拖选需要按住 Shift
	picker    *picker         // /model 选择器；非 nil 时替换输入框，接管按键
	effort    *effortPicker   // /effort 档位条；同上
	browser   *sessionBrowser // /resume 会话浏览器；同上
	mcpPicker *mcpPicker      // /mcp 服务端面板；同上
	skillPick *skillPicker    // /skills 技能面板；同上
	sug       suggestState    // 输入 / 开头时输入框上方的命令候选

	running    bool
	resuming   bool      // 正在恢复会话：此时不能发送，否则新提问会和恢复的历史交错
	switching  bool      // 正在切换模型：同上，禁止发送
	compacting bool      // 正在手动压缩上下文（/compact）：同上，且可用 Ctrl+C 取消
	mcpDone    bool      // 等待重建 agent（MCP 连好、/mcp 或 /skills 改了开关）；忙时推迟到本轮结束
	started    time.Time // 本轮开始时间，状态行显示已耗时
	// busySince、busyTotal 是状态栏“耗时”的数据：只累计 agent 干活的时间（一轮回答或一次 /compact，
	// 从开始到结束），空闲时不涨。busySince 为零表示当前空闲。
	busySince time.Time
	busyTotal time.Duration
	status    string // 状态行的临时提示，下次按键时清除
	statusOK  bool   // status 是成功提示（如已复制），用绿色而不是警告色显示
	// 状态栏的数据：本会话累计用量、最近一次请求的输入 token（上下文占用）、按当时单价累计的花费、整机资源。
	usage     usageTotals
	cost      costTotals
	ctxTokens int
	costUSD   float64 // 美元；/new 与恢复会话时清零，切模型后历史花费按原价保留
	res       Resources
	git       GitInfo
	// speed 是最近一次模型调用的输出速度（token/s），0 表示还没有可算的数据；
	// genStart、genEnd 是这次调用收到首个增量和消息完成的时刻。
	speed            float64
	genStart, genEnd time.Time
	// pending 是下一条消息待发送的图片（/image 附加），发送或 /image clear 后清空。
	pending []agent.Image

	// 对话区的拖选。选区用内容坐标记录；content 是当前送进 viewport 的完整文本，
	// plain 是它去掉样式后的逐行文本（按需计算，plainSrc 记录算的是哪一版）。
	sel selection
	// inSel 表示正在输入框里拖选：鼠标事件转给 textarea 自己的选区，松开时复制。
	inSel    bool
	content  string
	plain    []string
	plainSrc string
	clip     func(string) error // 写系统剪贴板；测试里替换成替身

	// spans[i] 是 entries[i] 在对话内容里占的行范围，点击时用来找到点中的条目。
	spans []rowSpan
	// callThink 是本次模型调用的思维链条目；usageThink 是刚结束的那次调用的思维链条目，
	// 等紧随 MessageCompleted 之后的 Usage 事件把思维链 token 数填进去。
	callThink, usageThink *entry
	now                   func() time.Time // 时钟；测试里替换成固定时间

	turn     int
	events   chan event.Event
	cancel   context.CancelFunc
	finished chan struct{} // agent goroutine 退出时关闭
	quit     chan struct{} // 通知 sink 不要再阻塞
	quitOnce sync.Once
}

// New 构造 Model。
func New(o Options) *Model {
	if o.Runner == nil && o.Disabled == "" {
		o.Disabled = "没有可用的 agent，无法发起对话。"
	}
	m := &Model{opts: o, follow: true, mouse: true, tools: map[string]*entry{}, quit: make(chan struct{}), clip: defaultClip, now: time.Now,
		res: Resources{Mem: -1, CPU: -1, ProcMB: -1}}

	m.vp = viewport.New()
	m.vp.FillHeight = true
	m.vp.MouseWheelEnabled = false

	m.in = textarea.New()
	// 只有首行显示提示符，续行用空格对齐。必须在 SetWidth 之前设置（layout 里每次重排都会重新 SetWidth）。
	m.in.SetPromptFunc(2, func(pi textarea.PromptInfo) string {
		if pi.LineNumber == 0 {
			return "❯ "
		}
		return "  "
	})
	m.in.Placeholder = "输入消息，Enter 发送，Ctrl+J 换行"
	m.in.ShowLineNumbers = false
	// 按内容增高，空时只占一行。MaxHeight 只管可见行数；MaxContentHeight 放得很大，
	// 否则输入会被卡在 MaxHeight 个逻辑行（组件为兼容旧行为的默认规则）。
	m.in.DynamicHeight, m.in.MinHeight, m.in.MaxHeight, m.in.MaxContentHeight = true, 1, inputMaxRows, 1<<20
	m.in.SetVirtualCursor(false) // 用真实终端光标，输入法的候选窗才会跟随
	// Enter 用于发送，换行改绑 Ctrl+J / Shift+Enter。
	m.in.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("ctrl+j", "shift+enter"))
	st := m.in.Styles()
	st.Focused.CursorLine, st.Blurred.CursorLine = lipgloss.NewStyle(), lipgloss.NewStyle()
	m.in.SetStyles(st)

	m.sp = spinner.New(spinner.WithSpinner(spinner.Dot))
	m.sp.Style = styFaint

	m.add(&entry{kind: kindNotice, text: welcomeText})
	for _, n := range o.Notices {
		m.add(&entry{kind: kindNotice, text: n})
	}
	for _, u := range o.Info.Unavailable {
		m.add(&entry{kind: kindNotice, text: u})
	}
	if o.Disabled != "" {
		m.add(&entry{kind: kindError, text: o.Disabled})
	}
	if len(o.Replay) > 0 {
		m.replay(o.Replay)
	}
	if o.ResumeNotice != "" {
		m.add(&entry{kind: kindNotice, text: o.ResumeNotice})
	}
	return m
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.in.Focus(), m.resourceCmd(0), m.gitCmd(0), m.mcpReadyCmd())
}

// mcpReadyMsg 表示后台 MCP 连接流程已结束（不论成败）。
type mcpReadyMsg struct{}

// mcpReadyCmd 等后台的 MCP 连接结束，再让 Update 在主线程里接上工具。
// 网络等待放在 Cmd 的 goroutine 里，界面启动不被它拖住。
func (m *Model) mcpReadyCmd() tea.Cmd {
	ch := m.opts.MCPReady
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		<-ch
		return mcpReadyMsg{}
	}
}

// applyMCP 把后台连上的 MCP 工具接进 agent，刷新信息栏。
// 只在空闲时调用：重建 agent 会搬移历史，进行中的一轮会丢掉它已经产生的消息。
// /skills 改了开关也走这里（mcpDone 只是“待重建”的标记）。
func (m *Model) applyMCP() {
	if !m.mcpDone || m.busy() {
		return
	}
	m.mcpDone = false
	info, notices := m.opts.ApplyMCP()
	m.opts.Info = info
	m.syncMCPServers()
	m.syncSkills()
	for _, n := range notices {
		m.add(&entry{kind: kindNotice, text: n})
	}
	m.refresh()
}

// busy 表示当前有不能被打断的操作：生成、压缩、恢复会话、切换模型。
func (m *Model) busy() bool { return m.running || m.compacting || m.resuming || m.switching }

func (m *Model) add(e *entry) *entry {
	e.dirty = true
	m.entries = append(m.entries, e)
	return e
}

// runningTools 返回当前仍在执行的工具名。同一批 tool call 并行时可能有多个。
func (m *Model) runningTools() []string {
	var names []string
	for _, e := range m.entries {
		if e.kind == kindTool && e.running {
			names = append(names, e.name)
		}
	}
	return names
}

// layout 按窗口尺寸重排各区域。
func (m *Model) layout() {
	m.in.SetWidth(max(m.w, 1)) // 也会按新宽度重算输入框的高度
	m.vp.SetWidth(max(m.w, 1))
	m.refresh()
}

// footerRows 是对话区下面占的行数：可选的活动行、分隔线、输入框（或选择器）、分隔线、三行状态栏。
// 空闲时只有 6 行（输入框一行）。
func (m *Model) footerRows() int {
	n := 1 + m.inputRows() + 1 + infoRows + m.suggestRows()
	if m.activityLine() != "" {
		n++
	}
	return n
}

// inputRows 是输入框区域当前占的行数：打开选择器或档位条时按它们的内容算。
func (m *Model) inputRows() int {
	if v, ok := m.overlay(); ok {
		return strings.Count(v, "\n") + 1
	}
	return m.in.Height()
}

// overlay 返回替换输入框的面板（模型选择器或思考档位条）；都没打开时 ok 为 false。
func (m *Model) overlay() (string, bool) {
	switch {
	case m.picker != nil:
		return m.pickerView(max(m.w, 1), pickerMaxRows), true
	case m.effort != nil:
		return m.effortView(max(m.w, 1)), true
	case m.browser != nil:
		return m.browserView(max(m.w, 1)), true
	case m.mcpPicker != nil:
		return m.mcpView(max(m.w, 1)), true
	case m.skillPick != nil:
		return m.skillView(max(m.w, 1)), true
	}
	return "", false
}

// pickerMaxRows 是选择器最多显示的行数；再多就在列表内部裁剪。
const pickerMaxRows = 8

// fit 让对话区高度跟上底部的变化（输入框增高、活动行出现或消失）。
// 跟随底部时保持贴底；否则保持当前位置，只在超出新范围时收敛。
func (m *Model) fit() {
	if m.w <= 0 || m.h <= 0 {
		return
	}
	h := max(m.h-m.footerRows(), 1)
	if h == m.vp.Height() {
		return
	}
	m.vp.SetHeight(h)
	if m.follow {
		m.vp.GotoBottom()
	} else {
		m.vp.SetYOffset(m.vp.YOffset()) // viewport 改高度时不收敛偏移，手动收敛一次
	}
}

// refresh 重新渲染 transcript；只有脏条目或宽度变化的条目会真正重绘。
// 暂停跟随（用户上滚）期间如果有新内容到达，会记下 unseen，状态行提示“有新内容”。
func (m *Model) refresh() {
	if m.w <= 0 {
		return
	}
	before := m.vp.TotalLineCount()
	parts := make([]string, len(m.entries))
	m.spans = m.spans[:0]
	row := 0
	for i, e := range m.entries {
		parts[i] = e.render(m.w)
		n := strings.Count(parts[i], "\n") + 1
		m.spans = append(m.spans, rowSpan{row, row + n})
		row += n + 1 // 条目之间空一行
	}
	content := strings.Join(parts, "\n\n")
	m.syncSel(content) // 内容对不上了就取消选区，必须在更新 m.content 之前
	m.content = content
	m.vp.SetContent(content)
	m.fit()
	if m.follow {
		m.vp.GotoBottom()
		m.unseen = false
	} else if m.vp.TotalLineCount() > before && before > 0 {
		m.unseen = true
	}
}

// Shutdown 在程序退出后调用：取消进行中的一轮，并等 agent goroutine 收尾，
// 这样 TurnFinished(cancelled) 也能被会话记录下来。wait 是最长等待时间。
func (m *Model) Shutdown(wait time.Duration) {
	if m.cancel != nil {
		m.cancel()
	}
	m.quitOnce.Do(func() { close(m.quit) })
	if m.finished != nil {
		select {
		case <-m.finished:
		case <-time.After(wait):
		}
	}
}
