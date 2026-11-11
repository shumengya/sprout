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

// Info 是 header 与 /tools 展示的信息，由 main 装配好传入。
type Info struct {
	Model       string
	Provider    string
	Cwd         string
	DataDir     string
	Tools       []ToolInfo // 实际可用的工具
	Unavailable []string   // 不可用的工具及原因，每项一行
	// Vision 表示当前模型支持图片输入；为 false 时仍可附加图片，但会提示会被省略。
	Vision bool
	// Effort 是当前思考档位；为空表示当前模型没有声明 effort，信息栏不显示。
	Effort string
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
	follow     bool // transcript 是否自动跟随底部
	unseen     bool // 暂停跟随期间是否有新内容到达

	showThink bool            // 是否展开思维链全文（/think 切换）
	mouse     bool            // 是否开启鼠标上报（/mouse 切换）；开启后滚轮可滚动，但终端自带的拖选需要按住 Shift
	picker    *picker         // /model 选择器；非 nil 时替换输入框，接管按键
	effort    *effortPicker   // /effort 档位条；同上
	browser   *sessionBrowser // /resume 会话浏览器；同上
	sug       suggestState    // 输入 / 开头时输入框上方的命令候选

	running   bool
	resuming  bool      // 正在恢复会话：此时不能发送，否则新提问会和恢复的历史交错
	switching bool      // 正在切换模型：同上，禁止发送
	started   time.Time // 本轮开始时间，状态行显示已耗时
	status    string    // 状态行的临时提示，下次按键时清除
	statusOK  bool      // status 是成功提示（如已复制），用绿色而不是警告色显示
	tokens    int       // 本会话累计 token
	// pending 是下一条消息待发送的图片（/image 附加），发送或 /image clear 后清空。
	pending []agent.Image

	// 对话区的拖选。选区用内容坐标记录；content 是当前送进 viewport 的完整文本，
	// plain 是它去掉样式后的逐行文本（按需计算，plainSrc 记录算的是哪一版）。
	sel      selection
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
	m := &Model{opts: o, follow: true, mouse: true, tools: map[string]*entry{}, quit: make(chan struct{}), clip: defaultClip, now: time.Now}

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

func (m *Model) Init() tea.Cmd { return m.in.Focus() }

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

// footerRows 是对话区下面占的行数：可选的活动行、分隔线、输入框（或选择器）、分隔线、信息栏。
// 空闲时只有 4 行（输入框一行）。
func (m *Model) footerRows() int {
	n := 1 + m.inputRows() + 1 + 1 + m.suggestRows()
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
