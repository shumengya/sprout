package tui

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/media"
)

// Update 只改状态、返回 Cmd，不做 IO。每条消息处理完都让对话区高度跟上底部：
// 打字会让输入框增高，按键会清掉状态提示，开始或结束一轮会让活动行出现或消失。
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	model, cmd := m.update(msg)
	// MCP 连好时若正忙，推迟到任意一条消息处理完、状态回到空闲时再接入；
	// 这样不必在每个“操作结束”的分支里各补一次。
	m.applyMCP()
	m.syncSuggest() // 输入框内容可能变了（打字、粘贴、提交后清空），候选跟着变
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

	case tea.PasteMsg:
		if m.pasteIntoBrowser(msg.Content) {
			return m, nil
		}
		// 粘贴的内容里带现有图片路径时，图片当场附加、输入框里只留 [文件名]，和对话区一致。
		if text := m.pasteImages(msg.Content); text != msg.Content {
			m.in.InsertString(text)
			return m, nil
		}

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

	case resourceMsg:
		m.res = msg.res
		return m, m.resourceCmd(time.Second) // 约定下一次采样；View 会随这次 Update 重绘，耗时因此每秒刷新

	case compactDoneMsg:
		m.onCompactDone(msg)
		m.refresh()
		return m, nil

	case sessionsLoadedMsg:
		m.onSessionsLoaded(msg)
		return m, nil

	case sessionOpDoneMsg:
		return m, m.onSessionOp(msg)

	case switchDoneMsg:
		m.onSwitched(msg)
		m.refresh()
		return m, nil

	case installDoneMsg:
		m.onInstalled(msg)
		m.refresh()
		return m, nil

	case mcpDoneMsg:
		m.onMCPDone(msg)
		m.refresh()
		return m, nil

	case skillDoneMsg:
		m.onSkillDone(msg)
		m.refresh()
		return m, nil

	case mcpReadyMsg:
		m.mcpDone = m.opts.ApplyMCP != nil
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
		if !m.running && !m.compacting { // 停止转发即停止动画，不会残留空转的定时器
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
	if m.effortKey(k) { // 档位条同上
		m.refresh()
		return m, nil
	}
	if handled, cmd := m.sessionKey(k); handled { // 会话浏览器同上
		m.refresh()
		return m, cmd
	}
	if handled, cmd := m.mcpKey(k); handled { // MCP 面板同上
		m.refresh()
		return m, cmd
	}
	if handled, cmd := m.skillKey(k); handled { // 技能面板同上
		m.refresh()
		return m, cmd
	}
	if handled, model, cmd := m.suggestKey(k); handled { // 有命令候选时，Tab ↑↓ Enter Esc 归它
		return model, cmd
	}
	m.status, m.statusOK = "", false
	m.clearSel() // 内容已经在选中时复制过了；继续操作就让高亮消失
	switch k.String() {
	case "ctrl+c":
		if m.running || m.compacting {
			m.cancel()
			m.status = "正在取消…"
			return m, nil
		}
		return m, tea.Quit
	case "esc":
		// Esc 取消生成，和多数终端 agent 一致；Ctrl+C 保留为习惯用法。
		// 候选列表或选择器打开时 Esc 已被它们接管，到这里说明没有面板占用。
		if m.running || m.compacting {
			m.cancel()
			m.status = "正在取消…"
		}
		return m, nil
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
		m.status = "上一轮还在生成，请等它结束，或按 Esc 取消"
		return m, nil
	case m.resuming:
		m.status = "正在恢复会话，请稍候"
		return m, nil
	case m.switching:
		m.status = "正在切换模型，请稍候"
		return m, nil
	case m.compacting:
		m.status = "正在压缩上下文，请稍候，或按 Esc 取消"
		return m, nil
	case m.opts.Disabled != "":
		m.status = "无法发送：" + firstLine(m.opts.Disabled)
		return m, nil
	}
	m.in.Reset()
	// 输入框里的 [文件名] 是粘贴时换上的显示形式，发出去要恢复成用户粘贴的原始路径。
	text, pending := expandPasted(text, m.pending)
	// 消息里直接写图片路径（贴一条截图路径、从资源管理器拖进来）也要当图片发出去，
	// 否则模型只看到一段路径文字，只能自己猜或去找 OCR。
	in, _, skipped := media.AutoAttach(agent.Input{Text: text, Images: pending})
	marks := imageListMarks(in.Images)
	m.pending = nil
	m.add(&entry{kind: kindUser, text: text, images: marks})
	if len(skipped) > 0 {
		m.add(&entry{kind: kindNotice, text: "有图片没能附加：\n  " + strings.Join(skipped, "\n  ")})
	}
	if len(in.Images) > 0 && !m.opts.Info.Vision {
		m.status = "当前模型没有标注 vision，图片会被省略（models.jsonc 里加 \"vision\": true 才不会）"
	}
	m.running, m.follow = true, true
	m.refresh()
	return m, m.startTurn(in)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
