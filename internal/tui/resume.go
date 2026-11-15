package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shumengya/sprout/internal/agent/event"
)

// SessionInfo 是会话浏览器里的一行。
type SessionInfo struct {
	ID      string
	Path    string // 会话文件；恢复、重命名、删除都按它定位
	Prompt  string // 第一条提问
	Name    string // 用户起的名字，没起过为空
	Cwd     string // 会话所属的工作目录
	Model   string
	Updated time.Time
	Turns   int
	Current bool // 是当前正在写的会话
}

// Title 是列表里显示的标题：起过名字用名字，否则用第一条提问。
func (s SessionInfo) Title() string {
	if s.Name != "" {
		return s.Name
	}
	return s.Prompt
}

// Restored 是恢复会话的结果：用来重建对话区的事件流，以及一句说明。
type Restored struct {
	Events []event.Event
	Notice string
}

// Sessions 让 TUI 列出、恢复和管理会话，由 main 提供实现。TUI 不直接依赖会话存储。
// 除 New 以外都可能读写磁盘，TUI 一律在 tea.Cmd 里调用。
type Sessions interface {
	// List 列出会话，最近使用的在前；all 为 false 时只列当前工作目录的。
	List(all bool) ([]SessionInfo, error)
	// Resume 恢复选择器指定的会话（会话文件路径、序号、ID 前缀或 latest）：
	// 让 agent 载入历史并接着写原来的会话文件。它会阻塞到 agent 空闲。
	Resume(selector string) (*Restored, error)
	// Rename 给会话起名，空名字表示清除。
	Rename(path, name string) error
	// Delete 删除会话及其图片；当前会话不能删。
	Delete(path string) error
	// New 开始一个空白会话：清空 agent 历史，之后的对话写进新的会话文件。不涉及网络，可以同步调用。
	New() error
}

// resumeDoneMsg 是恢复完成后发回 Update 的消息。
type resumeDoneMsg struct {
	res *Restored
	err error
}

// resumeCmd 处理 /resume。无参数时打开会话浏览器；带参数时直接恢复。
func (m *Model) resumeCmd(arg string) tea.Cmd {
	s := m.opts.Sessions
	switch {
	case s == nil:
		m.add(&entry{kind: kindNotice, text: "当前不能恢复会话（没有可用的 agent）。"})
	case m.running || m.resuming || m.compacting:
		m.status = "正在生成、恢复或压缩中，暂时不能切换会话"
	case arg == "":
		m.browser = &sessionBrowser{}
		m.status = ""
		return m.loadSessions()
	default:
		return m.startResume(arg)
	}
	return nil
}

// startResume 异步恢复 selector 指定的会话；恢复期间禁止发送。
func (m *Model) startResume(selector string) tea.Cmd {
	m.resuming = true
	m.status = "正在恢复会话…"
	s := m.opts.Sessions
	return func() tea.Msg {
		r, err := s.Resume(selector)
		return resumeDoneMsg{r, err}
	}
}

// newSessionCmd 处理 /new：清空对话区并开始新会话。失败时保留现有内容。
func (m *Model) newSessionCmd() {
	s := m.opts.Sessions
	switch {
	case s == nil:
		m.add(&entry{kind: kindNotice, text: "当前不能新建会话（没有可用的 agent）。"})
		return
	case m.running || m.resuming || m.switching || m.compacting:
		m.status = "正在生成、恢复、切换模型或压缩，暂时不能新建会话"
		return
	}
	if err := s.New(); err != nil {
		m.add(&entry{kind: kindError, text: "新建会话失败：" + err.Error()})
		return
	}
	m.entries, m.tools = nil, map[string]*entry{}
	m.usage, m.cost, m.ctxTokens = usageTotals{}, costTotals{}, 0 // 新会话从零开始计数
	m.resetBusy()
	m.resetSpeed()
	m.callThink, m.usageThink = nil, nil
	m.clearSel()
	m.add(&entry{kind: kindNotice, text: welcomeText})
	m.add(&entry{kind: kindNotice, text: "已开始新会话，之前的会话可以用 /resume 找回。"})
	m.unseen = false
}

// onResumed 处理恢复结果：成功就用保存的事件流重建对话区，失败则保留现有内容并报错。
func (m *Model) onResumed(msg resumeDoneMsg) {
	m.resuming, m.status = false, ""
	if msg.err != nil {
		m.add(&entry{kind: kindError, text: "恢复会话失败：" + msg.err.Error()})
		return
	}
	m.entries, m.tools = nil, map[string]*entry{}
	// 用量和上下文占用由重放的 usage / compacted 事件重新累计出来；
	// 恢复的会话不知道当初花了多久，耗时从 0 开始。
	// 会话记录里没有存单价，重放出来的花费按当前模型的价格估算。
	m.usage, m.cost, m.ctxTokens = usageTotals{}, costTotals{}, 0
	m.resetBusy()
	m.callThink, m.usageThink = nil, nil
	m.replay(msg.res.Events)
	m.add(&entry{kind: kindNotice, text: msg.res.Notice})
	m.unseen = false
}

// replay 把保存的事件流喂进对话区，复用实时事件的同一套渲染逻辑。
// 用户提问不走 onEvent（实时流程里它是 submit 时直接加的条目），这里单独补上。
func (m *Model) replay(events []event.Event) {
	for _, e := range events {
		if ts, ok := e.(event.TurnStarted); ok {
			m.add(&entry{kind: kindUser, text: ts.Prompt, images: imageMarks(ts.Images)})
			continue
		}
		m.onEvent(e)
	}
	m.running, m.cur, m.think, m.thinkShown, m.follow = false, nil, nil, false, true
	m.resetSpeed()
	m.callThink, m.usageThink = nil, nil
}
