package tui

import (
	"strings"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
)

// Update 只改状态、返回 Cmd，不做 IO。每条消息处理完都让对话区高度跟上底部：
// 打字会让输入框增高，按键会清掉状态提示，开始或结束一轮会让活动行出现或消失。
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	model, cmd := m.update(msg)
	m.fit()
	return model, cmd
}

func (m *Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.layout()
		return m, nil

	case tea.KeyPressMsg:
		return m.onKey(msg)

	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.scrollBy(-wheelStep)
		case tea.MouseWheelDown:
			m.scrollBy(wheelStep)
		}
		return m, nil

	case tea.MouseClickMsg:
		m.onMouseClick(msg)
		return m, nil

	case tea.MouseMotionMsg:
		m.onMouseMotion(msg)
		return m, nil

	case tea.MouseReleaseMsg:
		return m, m.onMouseRelease()

	case copiedMsg:
		m.onCopied(msg)
		return m, nil

	case tea.MouseMsg:
		// 其他鼠标事件（右键、中键等）不处理，也不转给输入框（否则会触发它自己的选区逻辑）。
		return m, nil

	case eventMsg:
		if msg.turn != m.turn { // 上一轮遗留的消息
			return m, nil
		}
		m.onEvent(msg.ev)
		m.refresh()
		return m, waitEvent(msg.turn, m.events)

	case resumeDoneMsg:
		m.onResumed(msg)
		m.refresh()
		return m, nil

	case switchDoneMsg:
		m.onSwitched(msg)
		m.refresh()
		return m, nil

	case installDoneMsg:
		m.onInstalled(msg)
		m.refresh()
		return m, nil

	case turnClosedMsg:
		if msg.turn != m.turn {
			return m, nil
		}
		if m.running { // 通道关了却没收到 TurnFinished，不能让界面一直停在“生成中”
			m.add(&entry{kind: kindError, text: "本轮意外结束，没有收到完成信号。"})
			m.finishTurn("")
			m.refresh()
		}
		return m, nil

	case spinner.TickMsg:
		if !m.running { // 停止转发即停止动画，不会残留空转的定时器
			return m, nil
		}
		var cmd tea.Cmd
		m.sp, cmd = m.sp.Update(msg)
		return m, cmd
	}

	var cmd tea.Cmd
	m.in, cmd = m.in.Update(msg)
	return m, cmd
}

func (m *Model) onKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if handled, cmd := m.pickerKey(k); handled { // 选择器打开时接管全部按键
		m.refresh()
		return m, cmd
	}
	m.status, m.statusOK = "", false
	m.clearSel() // 内容已经在选中时复制过了；继续操作就让高亮消失
	switch k.String() {
	case "ctrl+c":
		if m.running {
			m.cancel()
			m.status = "正在取消…"
			return m, nil
		}
		return m, tea.Quit
	case "ctrl+d":
		return m, tea.Quit
	case "enter":
		return m.submit()
	}
	if m.scrollKey(k.String()) {
		return m, nil
	}
	var cmd tea.Cmd
	m.in, cmd = m.in.Update(k)
	return m, cmd
}

func (m *Model) submit() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.in.Value())
	if text == "" {
		return m, nil
	}
	if handled, cmd := m.slash(text); handled {
		m.in.Reset()
		m.refresh()
		return m, cmd
	}
	switch {
	case m.running:
		m.status = "上一轮还在生成，请等它结束，或按 Ctrl+C 取消"
		return m, nil
	case m.resuming:
		m.status = "正在恢复会话，请稍候"
		return m, nil
	case m.switching:
		m.status = "正在切换模型，请稍候"
		return m, nil
	case m.opts.Disabled != "":
		m.status = "无法发送：" + firstLine(m.opts.Disabled)
		return m, nil
	}
	m.in.Reset()
	m.add(&entry{kind: kindUser, text: text})
	m.running, m.follow = true, true
	m.refresh()
	return m, m.startTurn(text)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
