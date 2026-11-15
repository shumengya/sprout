package tui

import tea "charm.land/bubbletea/v2"

// Updater 查询和安装新版本（启动检查与 /update）。两者都联网，TUI 只在 tea.Cmd 里调用。
type Updater interface {
	// Check 返回要提示给用户的一句话（有新版本时）；没有新版本、开发构建或查询失败时返回空串。
	Check() string
	// Update 检查并在有新版本时下载、校验、替换当前程序，返回给用户看的结果说明。
	Update() (string, error)
}

// updateNoticeMsg 是启动检查的结果。
type updateNoticeMsg struct{ text string }

// updateDoneMsg 是 /update 执行完的结果。
type updateDoneMsg struct {
	report string
	err    error
}

// updateCheckCmd 在后台查一次最新发布；界面启动不等它。
func (m *Model) updateCheckCmd() tea.Cmd {
	u := m.opts.Updater
	if u == nil {
		return nil
	}
	return func() tea.Msg { return updateNoticeMsg{text: u.Check()} }
}

// updateCmd 执行 /update。下载十几 MB 可能要一会儿，状态行一直显示进度提示。
func (m *Model) updateCmd() tea.Cmd {
	u := m.opts.Updater
	if u == nil {
		m.add(&entry{kind: kindError, text: "当前环境不支持自动更新。"})
		return nil
	}
	m.status, m.statusOK = "正在检查并下载更新…", false
	return func() tea.Msg {
		var r updateDoneMsg
		r.report, r.err = u.Update()
		return r
	}
}

func (m *Model) onUpdated(msg updateDoneMsg) {
	m.status, m.statusOK = "", false
	if msg.err != nil {
		m.add(&entry{kind: kindError, text: "更新失败：" + msg.err.Error()})
		return
	}
	m.add(&entry{kind: kindNotice, text: msg.report})
}
