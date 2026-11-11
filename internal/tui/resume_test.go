package tui

import (
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shumengya/sprout/internal/agent/event"
)

// fakeSessions 是可编程的会话存储替身。只用于测试。
type fakeSessions struct {
	list      []SessionInfo
	listErr   error
	res       *Restored
	resumeErr error
	gotSel    string
	newErr    error
	newCalled bool
}

func (f *fakeSessions) List() ([]SessionInfo, error) { return f.list, f.listErr }
func (f *fakeSessions) Resume(sel string) (*Restored, error) {
	f.gotSel = sel
	return f.res, f.resumeErr
}
func (f *fakeSessions) New() error {
	f.newCalled = true
	return f.newErr
}

func oldConversation() []event.Event {
	return []event.Event{
		event.TurnStarted{Prompt: "上次的提问"},
		event.ReasoningDelta{}, // 重放的是落盘事件，不会有增量；这里留一个空的确认不会出问题
		event.MessageCompleted{Content: "上次的回答", Reasoning: "上次的推理"},
		event.Usage{TotalTokens: 321},
		event.TurnFinished{Reason: event.ReasonStop},
	}
}

func sessionsModel(t *testing.T, f *fakeSessions) *Model {
	t.Helper()
	return newSized(t, Options{Runner: scriptRunner(), Sessions: f}, 120, 40)
}

// runCmd 执行一个 tea.Cmd 并把结果消息喂回 Update，模拟 Bubble Tea 的行为。
func runCmd(m *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	m.Update(cmd())
}

func TestResumeWithoutArgListsSessions(t *testing.T) {
	f := &fakeSessions{list: []SessionInfo{
		{ID: "abcd1234", Prompt: "修复登录", Model: "deepseek-flash", Turns: 3, Updated: time.Date(2026, 7, 27, 9, 5, 0, 0, time.Local)},
		{ID: "ef567890", Prompt: "写文档", Model: "deepseek-flash", Turns: 1, Updated: time.Date(2026, 7, 26, 18, 0, 0, 0, time.Local)},
	}}
	m := sessionsModel(t, f)
	submit(m, "/resume")
	mustContain(t, plain(m), "最近的会话", "1  07-27 09:05  3 轮", "修复登录", "2  07-26 18:00  1 轮", "写文档", "/resume 序号", "/resume abcd")
	if m.resuming || f.gotSel != "" {
		t.Error("只列出，不应开始恢复")
	}
}

func TestResumeListEmptyAndError(t *testing.T) {
	m := sessionsModel(t, &fakeSessions{})
	submit(m, "/resume")
	mustContain(t, plain(m), "还没有可恢复的会话")

	m = sessionsModel(t, &fakeSessions{listErr: errors.New("磁盘错误")})
	submit(m, "/resume")
	mustContain(t, plain(m), "读取会话列表失败：磁盘错误")
}

func TestResumeRebuildsTranscriptFromSavedEvents(t *testing.T) {
	f := &fakeSessions{res: &Restored{Events: oldConversation(), Notice: "已恢复会话 abcd1234（1 轮）"}}
	m := sessionsModel(t, f)
	begin(m, "当前屏幕上原有的内容") // 恢复要整个替换对话区
	m.running = false

	m.in.SetValue("/resume 2")
	_, cmd := m.Update(keyEnter)
	if !m.resuming || cmd == nil {
		t.Fatal("应进入恢复中状态并返回异步命令")
	}
	mustContain(t, plain(m), "正在恢复会话")
	runCmd(m, cmd)

	if f.gotSel != "2" || m.resuming {
		t.Fatalf("选择器 = %q resuming=%v", f.gotSel, m.resuming)
	}
	s := plain(m)
	mustContain(t, s, "上次的提问", "上次的回答", "已恢复会话 abcd1234（1 轮）", "321 token")
	mustNotContain(t, s, "当前屏幕上原有的内容", "正在恢复")
	if m.running {
		t.Error("重放不应让界面停在生成中")
	}
	if countKind(m, kindUser) != 1 || countKind(m, kindAssistant) != 1 {
		t.Errorf("应恢复出 1 条提问和 1 条回答: user=%d assistant=%d", countKind(m, kindUser), countKind(m, kindAssistant))
	}
}

func TestResumeFailureKeepsCurrentTranscript(t *testing.T) {
	f := &fakeSessions{resumeErr: errors.New("没有序号为 9 的会话（共 2 个）")}
	m := sessionsModel(t, f)
	begin(m, "还在的内容")
	m.running = false
	m.in.SetValue("/resume 9")
	_, cmd := m.Update(keyEnter)
	runCmd(m, cmd)
	mustContain(t, plain(m), "还在的内容", "恢复会话失败：没有序号为 9 的会话")
	if m.resuming {
		t.Error("失败后应退出恢复中状态")
	}
}

func TestCannotResumeWhileRunningOrResuming(t *testing.T) {
	f := &fakeSessions{res: &Restored{Notice: "ok"}}
	m := sessionsModel(t, f)
	m.running = true
	m.in.SetValue("/resume 1")
	if _, cmd := m.Update(keyEnter); cmd != nil || f.gotSel != "" {
		t.Fatal("生成中不能恢复会话")
	}
	mustContain(t, plain(m), "暂时不能切换会话")

	m.running, m.resuming = false, true
	m.in.SetValue("/resume 1")
	if _, cmd := m.Update(keyEnter); cmd != nil {
		t.Fatal("恢复中不能再次恢复")
	}
}

func TestSubmitIsBlockedWhileResuming(t *testing.T) {
	m := sessionsModel(t, &fakeSessions{})
	m.resuming = true
	m.in.SetValue("新的提问")
	if _, cmd := m.Update(keyEnter); cmd != nil || m.running {
		t.Fatal("恢复期间新提问会和恢复的历史交错，必须拒绝")
	}
	mustContain(t, plain(m), "正在恢复会话，请稍候")
	if m.in.Value() != "新的提问" {
		t.Error("输入内容应保留")
	}
}

func TestResumeUnavailableWithoutSessions(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 30)
	submit(m, "/resume")
	mustContain(t, plain(m), "当前不能恢复会话")
}

func TestStartupReplayShowsPreviousConversation(t *testing.T) {
	m := newSized(t, Options{
		Runner: scriptRunner(), Replay: oldConversation(), ResumeNotice: "已恢复最近的会话（1 轮）",
	}, 120, 40)
	s := plain(m)
	mustContain(t, s, "上次的提问", "上次的回答", "已恢复最近的会话（1 轮）")
	if m.running || m.turn != 0 {
		t.Errorf("启动重放不应产生进行中的轮次: running=%v turn=%d", m.running, m.turn)
	}
	// 恢复后可以正常发起新的一轮。
	m.in.SetValue("接着问")
	if _, cmd := m.Update(keyEnter); cmd == nil || !m.running {
		t.Error("重放之后应能继续对话")
	}
}

func TestReplayedToolCardsAreNotStuckRunning(t *testing.T) {
	events := []event.Event{
		event.TurnStarted{Prompt: "列目录"},
		event.MessageCompleted{ToolCalls: []event.ToolCall{{ID: "c1", Name: "bash", Arguments: `{"command":"ls"}`}}},
		event.ToolRequested{Call: event.ToolCall{ID: "c1", Name: "bash", Arguments: `{"command":"ls"}`}},
		event.ToolFinished{ID: "c1", Name: "bash", Command: "ls", Stdout: "a.go\n", Duration: 20 * time.Millisecond},
		event.MessageCompleted{Content: "有 a.go"},
		event.TurnFinished{Reason: event.ReasonStop},
	}
	m := newSized(t, Options{Runner: scriptRunner(), Replay: events}, 120, 40)
	mustContain(t, plain(m), "bash", "ls", "a.go", "有 a.go")
	mustNotContain(t, plain(m), "运行中")
	if len(m.runningTools()) != 0 {
		t.Error("重放出来的工具卡片不应还在运行")
	}
}

func TestResumeHelpMentionsResume(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 70)
	submit(m, "/help")
	mustContain(t, plain(m), "/resume")
}

func TestNewSessionClearsTranscriptAndHistory(t *testing.T) {
	f := &fakeSessions{}
	m := sessionsModel(t, f)
	begin(m, "之前的内容")
	m.running = false
	m.tokens = 500

	submit(m, "/new")
	if !f.newCalled {
		t.Fatal("应调用 New()")
	}
	s := plain(m)
	mustContain(t, s, "已开始新会话", "可以用 /resume 找回")
	mustNotContain(t, s, "之前的内容")
	if m.tokens != 0 {
		t.Errorf("token 计数应归零: %d", m.tokens)
	}
	if countKind(m, kindUser) > 0 || countKind(m, kindAssistant) > 0 {
		t.Error("对话区应已清空")
	}
}

func TestNewSessionFailureKeepsCurrentTranscript(t *testing.T) {
	f := &fakeSessions{newErr: errors.New("记录器不可用")}
	m := sessionsModel(t, f)
	begin(m, "保留的内容")
	m.running = false

	submit(m, "/new")
	mustContain(t, plain(m), "保留的内容", "新建会话失败：记录器不可用")
}

func TestCannotNewWhileRunningOrResuming(t *testing.T) {
	f := &fakeSessions{}
	m := sessionsModel(t, f)
	m.running = true
	submit(m, "/new")
	if f.newCalled {
		t.Error("生成中不能新建会话")
	}
	mustContain(t, plain(m), "暂时不能新建会话")

	m.running, m.resuming = false, true
	submit(m, "/new")
	if f.newCalled {
		t.Error("恢复中不能新建会话")
	}
}

func TestNewUnavailableWithoutSessions(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 30)
	submit(m, "/new")
	mustContain(t, plain(m), "当前不能新建会话")
}
