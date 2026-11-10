package tui

import tea "charm.land/bubbletea/v2"

// Installer 注册和卸载全局 sprout 命令。它会改用户 PATH、写文件，所以 TUI 在 tea.Cmd 里调用它，不在 Update 里做。
// 返回的是给用户看的结果说明。
type Installer interface {
	Install() (string, error)
	Uninstall() (string, error)
}

// installDoneMsg 是注册或卸载完成后发回 Update 的结果。
type installDoneMsg struct {
	report string
	err    error
}

// installCmd 执行 /install-sprout（uninstall 为 false）或 /uninstall-sprout。
func (m *Model) installCmd(uninstall bool) tea.Cmd {
	in := m.opts.Installer
	if in == nil {
		m.add(&entry{kind: kindError, text: "当前环境不支持注册全局命令。"})
		return nil
	}
	if uninstall {
		m.status = "正在卸载全局 sprout 命令…"
	} else {
		m.status = "正在注册全局 sprout 命令…"
	}
	m.statusOK = false
	return func() tea.Msg {
		var r installDoneMsg
		if uninstall {
			r.report, r.err = in.Uninstall()
		} else {
			r.report, r.err = in.Install()
		}
		return r
	}
}

func (m *Model) onInstalled(msg installDoneMsg) {
	m.status, m.statusOK = "", false
	if msg.err != nil {
		m.add(&entry{kind: kindError, text: msg.err.Error()})
		return
	}
	m.add(&entry{kind: kindNotice, text: msg.report})
}
