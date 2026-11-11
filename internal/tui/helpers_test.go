package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/event"
)

// fakeRunner 按脚本发事件。只用于测试，不在任何默认运行路径里。
type fakeRunner struct {
	run func(ctx context.Context, in agent.Input, sink event.Sink) error
}

func (f fakeRunner) Run(ctx context.Context, in agent.Input, sink event.Sink) error {
	return f.run(ctx, in, sink)
}

// scriptRunner 发出给定事件后返回。
func scriptRunner(events ...event.Event) fakeRunner {
	return fakeRunner{run: func(_ context.Context, _ agent.Input, sink event.Sink) error {
		for _, e := range events {
			sink.Emit(e)
		}
		return nil
	}}
}

var testInfo = Info{
	Model: "deepseek-chat", Provider: "deepseek", Cwd: `D:\SmyProjects\sprout`, DataDir: `D:\data\.sprout`,
	Tools: []ToolInfo{{"powershell", "PowerShell 7+  C:\\pwsh.exe"}, {"bash", "Git Bash  C:\\bash.exe"}},
}

// newSized 构造一个已经收到窗口尺寸的 Model，可以直接调用 Update / View。
func newSized(t *testing.T, o Options, w, h int) *Model {
	t.Helper()
	if o.Info.Model == "" {
		o.Info = testInfo
	}
	m := New(o)
	// 时钟默认冻结：思考耗时一律为 0，头部不会因为两个事件之间恰好跨过一个时钟刻度而多出“1秒”，
	// 测试结果不随机器快慢变化。要测耗时的用 withClock 拨动。
	frozen := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return frozen }
	m.Init() // 与真实程序一致：Bubble Tea 启动时先调用 Init，输入框的焦点在这里设置
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

func press(code rune, mod tea.KeyMod) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code, Mod: mod} }

var (
	keyEnter = press(tea.KeyEnter, 0)
	keyCtrlC = press('c', tea.ModCtrl)
	keyCtrlD = press('d', tea.ModCtrl)
	keyPgUp  = press(tea.KeyPgUp, 0)
	keyPgDn  = press(tea.KeyPgDown, 0)
)

func typed(s string) tea.KeyPressMsg {
	r := []rune(s)
	return tea.KeyPressMsg{Code: r[0], Text: s}
}

// plain 返回去掉 ANSI 样式后的整屏文本，便于断言。
func plain(m *Model) string { return ansi.Strip(m.View().Content) }

// apply 把一串事件直接喂给 Model（相当于 eventMsg 到达），并刷新视图。
func apply(m *Model, events ...event.Event) {
	for _, e := range events {
		m.Update(eventMsg{turn: m.turn, ev: e})
	}
}

// begin 模拟用户提交了一条消息，但不真正启动 agent。
func begin(m *Model, text string) {
	m.turn++
	m.running, m.follow = true, true
	m.add(&entry{kind: kindUser, text: text})
	m.refresh()
}

// mustBeIdle 断言本轮已结束：不在运行，画面上也没有“Ctrl+C 取消”。空闲时不再显示“空闲”二字，活动行整行消失。
func mustBeIdle(t *testing.T, m *Model) {
	t.Helper()
	if m.running {
		t.Error("应已结束本轮")
	}
	mustNotContain(t, plain(m), "Ctrl+C 取消", "生成中", "思考中")
}

func mustContain(t *testing.T, s string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(s, w) {
			t.Errorf("画面缺少 %q:\n%s", w, s)
		}
	}
}

func mustNotContain(t *testing.T, s string, nots ...string) {
	t.Helper()
	for _, n := range nots {
		if strings.Contains(s, n) {
			t.Errorf("画面不应出现 %q:\n%s", n, s)
		}
	}
}
