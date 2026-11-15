package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/event"
)

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestCtrlCWhenIdleQuits(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 24)
	if _, cmd := m.Update(keyCtrlC); !isQuit(cmd) {
		t.Fatal("空闲时 Ctrl+C 应退出")
	}
}

func TestCtrlDQuits(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 24)
	if _, cmd := m.Update(keyCtrlD); !isQuit(cmd) {
		t.Fatal("Ctrl+D 应退出")
	}
	// 生成中也直接退出；善后由 Shutdown 负责。
	m.running = true
	if _, cmd := m.Update(keyCtrlD); !isQuit(cmd) {
		t.Fatal("生成中 Ctrl+D 也应退出")
	}
}

func TestCtrlCWhileRunningCancelsInsteadOfQuitting(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 24)
	cancelled := false
	m.running = true
	m.cancel = func() { cancelled = true }

	_, cmd := m.Update(keyCtrlC)
	if isQuit(cmd) || !cancelled {
		t.Fatalf("生成中 Ctrl+C 应取消本轮而不是退出：cancelled=%v", cancelled)
	}
	mustContain(t, plain(m), "正在取消")
}

func TestEscWhileRunningCancelsInsteadOfQuitting(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 24)
	cancelled := false
	m.running = true
	m.cancel = func() { cancelled = true }

	_, cmd := m.Update(keyEsc)
	if isQuit(cmd) || !cancelled {
		t.Fatalf("生成中 Esc 应取消本轮而不是退出：cancelled=%v", cancelled)
	}
	mustContain(t, plain(m), "正在取消")
}

func TestEscWhenIdleDoesNothing(t *testing.T) {
	// 空闲时 Esc 不退出程序，也不留下状态提示：它常常是取消选区、关掉面板留下的多余按键。
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 24)
	if _, cmd := m.Update(keyEsc); isQuit(cmd) {
		t.Fatal("空闲时 Esc 不应退出")
	}
	if m.status != "" {
		t.Fatalf("空闲时 Esc 不应留下提示，得到 %q", m.status)
	}
}

func TestEscClosesSuggestionInsteadOfCancelling(t *testing.T) {
	// 候选列表打开时 Esc 归候选列表，不能顺手把正在跑的一轮也取消掉。
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 24)
	cancelled := false
	m.running = true
	m.cancel = func() { cancelled = true }
	m.in.SetValue("/th")
	m.syncSuggest()

	m.Update(keyEsc)
	if cancelled {
		t.Fatal("候选列表打开时 Esc 只应关闭候选")
	}
	if len(m.sug.items) != 0 {
		t.Fatalf("Esc 后候选应关闭，仍有 %d 项", len(m.sug.items))
	}
}

func TestTypingAndNewline(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 24)
	for _, k := range []tea.KeyPressMsg{typed("你"), typed("好"), press('j', tea.ModCtrl), typed("x")} {
		m.Update(k)
	}
	if got := m.in.Value(); got != "你好\nx" {
		t.Fatalf("输入框内容 = %q，Ctrl+J 应换行", got)
	}
}

func TestEnterOnEmptyInputDoesNothing(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 24)
	m.in.SetValue("   \n ")
	_, cmd := m.Update(keyEnter)
	if cmd != nil || m.running || countKind(m, kindUser) != 0 {
		t.Fatal("空白输入不应发送")
	}
}

func TestEnterSubmitsAndClearsInput(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 24)
	m.in.SetValue("  你好  ")
	_, cmd := m.Update(keyEnter)
	if cmd == nil || !m.running || m.in.Value() != "" {
		t.Fatalf("应启动一轮并清空输入框：running=%v value=%q", m.running, m.in.Value())
	}
	if m.entries[len(m.entries)-1].text != "你好" {
		t.Errorf("用户条目应去掉首尾空白，得到 %q", m.entries[len(m.entries)-1].text)
	}
}

// 生成中提交的消息进队列：输入框清空，对话区标出“排队中”，本轮结束后才发出。
func TestSubmitWhileRunningQueuesTheMessage(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 24)
	m.running = true
	m.in.SetValue("第二条")
	_, cmd := m.Update(keyEnter)
	if cmd != nil {
		t.Fatal("排队时不应立刻启动新一轮")
	}
	if m.in.Value() != "" {
		t.Errorf("入队后输入框应清空，得到 %q", m.in.Value())
	}
	if len(m.queue) != 1 || m.queue[0].in.Text != "第二条" {
		t.Fatalf("队列 = %+v", m.queue)
	}
	mustContain(t, plain(m), "第二条", "排队中", "已排队（第 1 条）")
	if n := m.turns(); n != 0 {
		t.Errorf("排队的那条不算一轮，turns = %d", n)
	}
	// 再提交一条：排在后面。
	m.in.SetValue("第三条")
	m.Update(keyEnter)
	if len(m.queue) != 2 || m.queue[1].in.Text != "第三条" {
		t.Fatalf("第二条排队失败: %+v", m.queue)
	}
	mustContain(t, plain(m), "已排队（第 2 条）")
}

// 本轮结束后队首自动发出，排队标记换回普通用户消息样式。
func TestQueuedMessageStartsAfterTurnFinishes(t *testing.T) {
	got := make(chan string, 4)
	m := newSized(t, Options{Runner: fakeRunner{run: func(_ context.Context, in agent.Input, sink event.Sink) error {
		got <- in.Text
		sink.Emit(event.TurnFinished{Reason: event.ReasonStop})
		return nil
	}}}, 80, 24)
	begin(m, "第一条")
	m.in.SetValue("第二条")
	m.Update(keyEnter)
	if len(m.queue) != 1 {
		t.Fatalf("应已排队: %+v", m.queue)
	}
	// 上一轮结束：Update 末尾把队首发出去。
	_, cmd := m.Update(eventMsg{turn: m.turn, ev: event.TurnFinished{Reason: event.ReasonStop}})
	if len(m.queue) != 0 || !m.running || cmd == nil {
		t.Fatalf("队首应已发出：queue=%d running=%v cmd=%v", len(m.queue), m.running, cmd != nil)
	}
	mustContain(t, plain(m), "第二条")
	if strings.Contains(plain(m), "排队中") {
		t.Error("发出后不应再显示“排队中”")
	}
	// Runner 在 Cmd 里启动（Bubble Tea 在 Update 之外执行），这里手动跑一遍批量 Cmd。
	runCmds(cmd)
	select {
	case text := <-got:
		if text != "第二条" {
			t.Errorf("队列里的内容应发给 agent，得到 %q", text)
		}
	case <-time.After(2 * time.Second):
		t.Error("队首没有真正调用 Runner")
	}
}

// runCmds 执行一个 Cmd，遇到 Batch 就递归展开。
// 里层的每个 Cmd 都放进 goroutine：startTurn 的批里有 waitEvent，它会一直等事件，不能同步跑。
func runCmds(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			go runCmds(c)
		}
	}
}

func TestDisabledBlocksSendingButKeepsInput(t *testing.T) {
	m := newSized(t, Options{Disabled: "未配置 API Key\n请设置 DEEPSEEK_API_KEY"}, 80, 24)
	mustContain(t, plain(m), "未配置 API Key", "请设置 DEEPSEEK_API_KEY")
	m.in.SetValue("你好")
	_, cmd := m.Update(keyEnter)
	if cmd != nil || m.running || countKind(m, kindUser) != 0 {
		t.Fatal("禁用状态下不应发起对话")
	}
	if m.in.Value() != "你好" {
		t.Error("输入内容应保留")
	}
	mustContain(t, plain(m), "无法发送：未配置 API Key")
}

func TestNilRunnerIsDisabledNotCrash(t *testing.T) {
	m := newSized(t, Options{}, 80, 24)
	m.in.SetValue("hi")
	if _, cmd := m.Update(keyEnter); cmd != nil || m.running {
		t.Fatal("没有 Runner 时不应启动")
	}
	mustContain(t, plain(m), "没有可用的 agent")
}
