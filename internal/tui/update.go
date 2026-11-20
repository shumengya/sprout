package tui

import (
	"errors"
	"fmt"
	"strings"
	"sync"
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
	// models.jsonc 改过也一样：等空闲再重新读取，排队的消息会等它读完、用新配置发出。
	if r := m.startReload(); r != nil {
		cmd = tea.Batch(cmd, r)
	}
	// 队列同理：不管本轮是正常结束、出错还是被取消，回到空闲就在这里发出下一条。
	if q := m.startQueued(); q != nil {
		cmd = tea.Batch(cmd, q)
	}
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

	case gitMsg:
		m.git = msg.info
		return m, m.gitCmd(gitInterval)

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

	case treeLoadedMsg:
		m.onTreeLoaded(msg)
		m.refresh()
		return m, nil

	case sessionOpDoneMsg:
		return m, m.onSessionOp(msg)

	case switchDoneMsg:
		m.onSwitched(msg)
		m.refresh()
		return m, nil

	case updateNoticeMsg:
		// 启动检查只在有新版本时给一句话；没结果就什么都不做。
		if msg.text != "" {
			m.add(&entry{kind: kindNotice, text: msg.text})
			m.refresh()
		}
		return m, nil

	case updateDoneMsg:
		m.onUpdated(msg)
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

	case mcpChangedMsg:
		// 只置标记：真正的重建在 Update 末尾的空闲检查里做，生成中不会换掉工具。
		m.mcpDone = m.opts.ApplyMCP != nil
		return m, m.mcpChangedCmd()

	case modelsTickMsg:
		if msg.changed {
			m.reloadDue = true // 同上，只置标记
		}
		return m, m.modelsWatchCmd()

	case modelsReloadedMsg:
		m.onModelsReloaded(msg)
		m.refresh()
		return m, nil

	case askMsg:
		m.onAsk(msg.a)
		return m, m.askCmd()

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
	if m.askKey(k) { // 服务端请求输入时优先接管，否则 MCP 调用会一直等不到回答
		m.refresh()
		return m, nil
	}
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
	if handled, cmd := m.treeKey(k); handled { // 会话树面板同上
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
	// 手动触发技能：/技能名 [任务]。名字对不上任何技能时按普通提问处理，
	// 这样 "/etc/hosts 是什么" 这类输入不会被打断。
	invocation, skillName, skillErr := m.skillInvocation(text)
	if skillErr != nil {
		// 技能存在但用不了（已停用、读不出正文）：说明原因，不发起这一轮。
		m.add(&entry{kind: kindNotice, text: skillErr.Error()})
		m.refresh()
		return m, nil
	}
	if invocation != "" {
		in.Text = invocation
	}
	var notes []string
	marks := imageListMarks(in.Images)
	m.pending = nil
	e := m.add(&entry{kind: kindUser, text: text, images: marks})
	if skillName != "" {
		notes = append(notes, "已手动触发技能 "+skillName)
	}
	if len(skipped) > 0 {
		notes = append(notes, "有图片没能附加：\n  "+strings.Join(skipped, "\n  "))
	}
	for _, n := range notes {
		m.add(&entry{kind: kindNotice, text: n})
	}
	// 正在生成时先入队：想补充信息、追加要求，不必等本轮结束再把话重打一遍。
	// 内容按提交这一刻的样子存好（图片、技能都已展开）。内核在当前这批工具执行完后取走，
	// 接在工具结果后面交给模型；模型这一步已经在给最终答案时，等本轮结束后作为新的一轮发出。
	if m.running {
		e.queued = true
		e.touch()
		n := m.queue.push(in)
		m.status = fmt.Sprintf("已排队（第 %d 条），当前步骤完成后自动发送", n)
		m.refresh()
		return m, nil
	}
	if len(in.Images) > 0 && !m.opts.Info.Vision {
		m.status = "当前模型没有标注 vision，图片会被省略（models.jsonc 里加 \"vision\": true 才不会）"
	}
	m.running, m.follow = true, true
	m.refresh()
	return m, m.startTurn(in)
}

// inputQueue 是生成中提交、还没交给内核的输入，按提交顺序排列。界面线程追加；
// agent goroutine 在一批工具执行完后经 Input.Queued 一次取走全部；没被取走的由界面在本轮结束后逐条发出。
// 两个线程都会碰它，所以加锁。对话区里对应的条目靠 entry.queued 标记，顺序与这里一致。
type inputQueue struct {
	mu    sync.Mutex
	items []agent.Input
}

// push 追加一条，返回追加后的条数。
func (q *inputQueue) push(in agent.Input) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = append(q.items, in)
	return len(q.items)
}

// take 取走全部，供 Input.Queued 使用。
func (q *inputQueue) take() []agent.Input {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.items
	q.items = nil
	return out
}

// pop 取出队首一条。
func (q *inputQueue) pop() (agent.Input, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return agent.Input{}, false
	}
	in := q.items[0]
	q.items = q.items[1:]
	return in, true
}

func (q *inputQueue) count() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

func (q *inputQueue) clear() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = nil
}

// unqueueFirst 把对话区里最早一条“排队中”的用户消息换回正常样式并返回它。
// 队列是先进先出的，最早那条就是刚被取走或发出的那条。
func (m *Model) unqueueFirst() *entry {
	for _, e := range m.entries {
		if e.queued {
			e.queued = false
			e.touch()
			return e
		}
	}
	return nil
}

// startQueued 在本轮结束后发出队首那条。还在忙或队列为空时返回 nil。
// 排队内容是用户主动提交的，上一轮被取消也照发：取消的是那一轮的回答，不是这些提问。
func (m *Model) startQueued() tea.Cmd {
	if m.running || m.resuming || m.switching || m.compacting {
		return nil
	}
	in, ok := m.queue.pop()
	if !ok {
		return nil
	}
	m.unqueueFirst()
	if n := m.queue.count(); n > 0 {
		m.status = fmt.Sprintf("已发送排队消息，还有 %d 条等待", n)
	}
	m.running, m.follow = true, true
	m.refresh()
	return m.startTurn(in)
}

// skillInvocation 把 "/技能名 任务" 展开成发给模型的提问，返回展开后的正文与技能名。
// 不是手动触发时返回空串与 nil；技能存在但已停用等真正的错误才返回 err。
func (m *Model) skillInvocation(text string) (invocation, name string, err error) {
	if m.opts.Skills == nil {
		return "", "", nil
	}
	cmd, task, _ := strings.Cut(text, " ")
	// 多行输入不当作触发：技能名后面跟一大段粘贴内容时，更可能是在提问。
	if !strings.HasPrefix(cmd, "/") || len(cmd) < 2 || strings.ContainsAny(text, "\r\n") {
		return "", "", nil
	}
	name = strings.TrimPrefix(cmd, "/")
	out, err := m.opts.Skills.Trigger(name, task)
	if errors.Is(err, ErrUnknownSkill) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	return out, name, nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
