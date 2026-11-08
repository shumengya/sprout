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

	"github.com/shumengya/sprout/internal/agent/event"
)

// Runner 是 TUI 眼中的内核：处理一条输入，过程以事件发给 sink。*agent.Agent 满足它。
type Runner interface {
	Run(ctx context.Context, prompt string, sink event.Sink) error
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
}

// Options 是构造 Model 所需的全部输入。
type Options struct {
	Runner Runner
	Info   Info
	// Disabled 非空表示当前不能发起对话（如缺少 API Key），提交时不会调用 Runner。
	Disabled string
	// Notices 是启动时显示在 transcript 顶部的提示。
	Notices []string
}

const (
	headerH = 4 // 三行信息 + 分隔线
	inputH  = 3
	footerH = 2 + inputH // 分隔线 + 状态行 + 输入框
)

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

	showThink bool // 是否展开思维链全文（/think 切换）
	mouse     bool // 是否开启鼠标上报（/mouse 切换）；开启后滚轮可滚动，但终端自带的拖选需要按住 Shift

	running bool
	started time.Time // 本轮开始时间，状态行显示已耗时
	curTool string    // 正在执行的工具名，状态行显示用
	status  string    // 状态行的临时提示，下次按键时清除
	tokens  int       // 本会话累计 token

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
	m := &Model{opts: o, follow: true, mouse: true, tools: map[string]*entry{}, quit: make(chan struct{})}

	m.vp = viewport.New()
	m.vp.FillHeight = true
	m.vp.MouseWheelEnabled = false

	m.in = textarea.New()
	// 只有首行显示提示符，续行用空格对齐。必须在 SetWidth 之前设置（layout 里每次重排都会重新 SetWidth）。
	m.in.SetPromptFunc(2, func(pi textarea.PromptInfo) string {
		if pi.LineNumber == 0 {
			return "> "
		}
		return "  "
	})
	m.in.Placeholder = "输入消息，Enter 发送，Ctrl+J 换行"
	m.in.ShowLineNumbers = false
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
	return m
}

func (m *Model) Init() tea.Cmd { return m.in.Focus() }

func (m *Model) add(e *entry) *entry {
	e.dirty = true
	m.entries = append(m.entries, e)
	return e
}

// layout 按窗口尺寸重排各区域。
func (m *Model) layout() {
	m.in.SetWidth(max(m.w, 1))
	m.in.SetHeight(inputH)
	m.vp.SetWidth(max(m.w, 1))
	m.vp.SetHeight(max(m.h-headerH-footerH, 1))
	m.refresh()
}

// refresh 重新渲染 transcript；只有脏条目或宽度变化的条目会真正重绘。
// 暂停跟随（用户上滚）期间如果有新内容到达，会记下 unseen，状态行提示“有新内容”。
func (m *Model) refresh() {
	if m.w <= 0 {
		return
	}
	before := m.vp.TotalLineCount()
	parts := make([]string, len(m.entries))
	for i, e := range m.entries {
		parts[i] = e.render(m.w, m.showThink)
	}
	m.vp.SetContent(strings.Join(parts, "\n\n"))
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
