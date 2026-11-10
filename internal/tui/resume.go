package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shumengya/sprout/internal/agent/event"
)

// SessionInfo 是 /resume 列表里的一行。
type SessionInfo struct {
	ID      string
	Prompt  string // 第一条提问
	Model   string
	Updated time.Time
	Turns   int
}

// Restored 是恢复会话的结果：用来重建对话区的事件流，以及一句说明。
type Restored struct {
	Events []event.Event
	Notice string
}

// Sessions 让 TUI 列出并恢复会话，由 main 提供实现。TUI 不直接依赖会话存储。
type Sessions interface {
	List() ([]SessionInfo, error)
	// Resume 恢复选择器指定的会话（序号、ID 前缀或 latest）：让 agent 载入历史并接着写原来的会话文件。
	// 它会阻塞到 agent 空闲，所以 TUI 在 tea.Cmd 里调用它。
	Resume(selector string) (*Restored, error)
}

// resumeDoneMsg 是恢复完成后发回 Update 的消息。
type resumeDoneMsg struct {
	res *Restored
	err error
}

// resumeCmd 处理 /resume。无参数时列出会话；带参数时返回一个异步恢复的命令。
func (m *Model) resumeCmd(arg string) tea.Cmd {
	s := m.opts.Sessions
	switch {
	case s == nil:
		m.add(&entry{kind: kindNotice, text: "当前不能恢复会话（没有可用的 agent）。"})
	case m.running || m.resuming:
		m.status = "正在生成或恢复中，暂时不能切换会话"
	case arg == "":
		m.add(&entry{kind: kindNotice, text: sessionListText(s)})
	default:
		m.resuming = true
		m.status = "正在恢复会话…"
		return func() tea.Msg {
			r, err := s.Resume(arg)
			return resumeDoneMsg{r, err}
		}
	}
	return nil
}

func sessionListText(s Sessions) string {
	list, err := s.List()
	switch {
	case err != nil:
		return "读取会话列表失败：" + err.Error()
	case len(list) == 0:
		return "当前目录下还没有可恢复的会话。"
	}
	var b strings.Builder
	b.WriteString("当前目录下最近的会话：")
	for i, it := range list {
		fmt.Fprintf(&b, "\n  %d  %s  %d 轮  %s  %s", i+1, it.Updated.Format("01-02 15:04"), it.Turns, it.Model, it.Prompt)
	}
	b.WriteString("\n用 /resume 序号 恢复，也可以写 ID 前缀（如 /resume " + shortID(list[0].ID) + "）。")
	return b.String()
}

func shortID(id string) string { return id[:min(len(id), 4)] }

// onResumed 处理恢复结果：成功就用保存的事件流重建对话区，失败则保留现有内容并报错。
func (m *Model) onResumed(msg resumeDoneMsg) {
	m.resuming, m.status = false, ""
	if msg.err != nil {
		m.add(&entry{kind: kindError, text: "恢复会话失败：" + msg.err.Error()})
		return
	}
	m.entries, m.tools, m.tokens = nil, map[string]*entry{}, 0
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
			m.add(&entry{kind: kindUser, text: ts.Prompt})
			continue
		}
		m.onEvent(e)
	}
	m.running, m.cur, m.think, m.thinkShown, m.follow = false, nil, nil, false, true
	m.callThink, m.usageThink = nil, nil
}
