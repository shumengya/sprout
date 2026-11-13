package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
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

func TestSubmitWhileRunningIsRejectedAndKeepsInput(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 24)
	m.running = true
	m.in.SetValue("第二条")
	_, cmd := m.Update(keyEnter)
	if cmd != nil || m.in.Value() != "第二条" {
		t.Fatal("生成中不应发送，且输入框内容要保留")
	}
	mustContain(t, plain(m), "上一轮还在生成")
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
