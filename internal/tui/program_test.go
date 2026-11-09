package tui

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shumengya/sprout/internal/agent/event"
)

// probe 在每次 Update 之后检查 done，满足就退出程序。
// 检查发生在程序自己的 goroutine 里，所以读 Model 状态没有竞争。
type probe struct {
	*Model
	done func(*Model) bool
}

func (p probe) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := p.Model.Update(msg)
	if p.done(p.Model) {
		return p, tea.Batch(cmd, tea.Quit)
	}
	return p, cmd
}

// runProgram 用真实的 tea.Program 跑 m：禁用输入、丢弃输出，script 在另一个 goroutine 里
// 通过 send 注入按键。超时则失败，避免测试挂死。返回后可以安全读取 m。
func runProgram(t *testing.T, m *Model, script func(send func(tea.Msg)), done func(*Model) bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// 输出不是 TTY 时程序会用自己的 width/height（默认 0）发初始 WindowSizeMsg，
	// 手动 Send 的尺寸会被它覆盖，所以必须通过 WithWindowSize 设置。
	p := tea.NewProgram(probe{m, done}, tea.WithInput(nil), tea.WithOutput(io.Discard),
		tea.WithContext(ctx), tea.WithWindowSize(100, 30))
	go script(p.Send)
	if _, err := p.Run(); err != nil {
		t.Fatalf("程序异常结束: %v", err)
	}
}

func finishedOneTurn(m *Model) bool { return m.turn >= 1 && !m.running }

func newProgModel(r Runner) *Model {
	m := New(Options{Runner: r, Info: testInfo})
	m.in.SetValue("看一下目录")
	return m
}

func TestProgramFullTurnThroughRealEventLoop(t *testing.T) {
	r := scriptRunner(
		event.TurnStarted{Prompt: "看一下目录"},
		event.MessageDelta{Text: "我来"}, event.MessageDelta{Text: "看看"},
		event.MessageCompleted{Content: "我来看看", ToolCalls: []event.ToolCall{{ID: "c1", Name: "powershell"}}},
		requested("c1", "powershell", `{"command":"Get-ChildItem"}`),
		event.ToolFinished{ID: "c1", Name: "powershell", Command: "Get-ChildItem", Stdout: "main.go\n", ExitCode: 0},
		event.MessageDelta{Text: "入口是 main.go"},
		event.MessageCompleted{Content: "入口是 main.go"},
		event.TurnFinished{Reason: event.ReasonStop},
	)
	m := newProgModel(r)
	runProgram(t, m, func(send func(tea.Msg)) { send(keyEnter) }, finishedOneTurn)

	s := plain(m)
	mustContain(t, s, "看一下目录", "我来看看", "powershell", "Get-ChildItem", "1 行，exit 0", "main.go", "入口是 main.go", "空闲")
	if countKind(m, kindAssistant) != 2 || countKind(m, kindTool) != 1 {
		t.Errorf("条目数不对: assistant=%d tool=%d", countKind(m, kindAssistant), countKind(m, kindTool))
	}
}

func TestProgramPassesPromptToRunner(t *testing.T) {
	got := make(chan string, 1)
	r := fakeRunner{run: func(_ context.Context, prompt string, sink event.Sink) error {
		got <- prompt
		sink.Emit(event.TurnFinished{Reason: event.ReasonStop})
		return nil
	}}
	m := newProgModel(r)
	runProgram(t, m, func(send func(tea.Msg)) { send(keyEnter) }, finishedOneTurn)
	if p := <-got; p != "看一下目录" {
		t.Fatalf("Runner 收到的 prompt = %q", p)
	}
}

func TestProgramCtrlCCancelsRunningTurn(t *testing.T) {
	started := make(chan struct{})
	r := fakeRunner{run: func(ctx context.Context, _ string, sink event.Sink) error {
		close(started)
		<-ctx.Done() // 阻塞到被取消，模拟进行中的 HTTP 请求或工具
		sink.Emit(event.TurnFinished{Reason: event.ReasonCancelled})
		return ctx.Err()
	}}
	m := newProgModel(r)
	runProgram(t, m, func(send func(tea.Msg)) {
		send(keyEnter)
		<-started
		send(keyCtrlC)
	}, finishedOneTurn)

	mustContain(t, plain(m), "已取消本轮对话", "空闲")
}

func TestProgramShutdownWaitsForAgentToFinish(t *testing.T) {
	started := make(chan struct{})
	var finalized atomic.Bool
	r := fakeRunner{run: func(ctx context.Context, _ string, sink event.Sink) error {
		close(started)
		<-ctx.Done()
		time.Sleep(150 * time.Millisecond) // 取消后还要收尾，比如写会话记录
		finalized.Store(true)
		sink.Emit(event.TurnFinished{Reason: event.ReasonCancelled})
		return ctx.Err()
	}}
	m := newProgModel(r)
	runProgram(t, m, func(send func(tea.Msg)) {
		send(keyEnter)
		<-started
		send(keyCtrlD) // 生成中直接退出
	}, func(*Model) bool { return false })

	m.Shutdown(5 * time.Second)
	if !finalized.Load() {
		t.Fatal("Shutdown 应取消进行中的一轮并等 agent 收尾完成，否则最后的 TurnFinished 不会被会话记录")
	}
}

func TestShutdownGivesUpAfterTimeout(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 24)
	m.finished = make(chan struct{}) // 永远不会关闭
	begin := time.Now()
	m.Shutdown(100 * time.Millisecond)
	if time.Since(begin) > 2*time.Second {
		t.Fatal("agent 不响应时 Shutdown 也必须在超时后返回，不能让退出卡死")
	}
}

func TestShutdownWithoutTurnIsImmediate(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 24)
	m.Shutdown(time.Hour)
	m.Shutdown(time.Hour) // 重复调用无害
}

func TestProgramRunnerPanicBecomesErrorEntry(t *testing.T) {
	r := fakeRunner{run: func(context.Context, string, event.Sink) error { panic("炸了") }}
	m := newProgModel(r)
	runProgram(t, m, func(send func(tea.Msg)) { send(keyEnter) }, finishedOneTurn)
	mustContain(t, plain(m), "内部错误: 炸了", "空闲")
}

func TestProgramRunnerReturningWithoutFinishedDoesNotHang(t *testing.T) {
	r := fakeRunner{run: func(context.Context, string, event.Sink) error { return nil }}
	m := newProgModel(r)
	runProgram(t, m, func(send func(tea.Msg)) { send(keyEnter) }, finishedOneTurn)
	mustContain(t, plain(m), "意外结束", "空闲")
}

func TestProgramSecondTurnAfterFirst(t *testing.T) {
	var n atomic.Int32
	r := fakeRunner{run: func(_ context.Context, prompt string, sink event.Sink) error {
		n.Add(1)
		sink.Emit(event.MessageDelta{Text: "回答:" + prompt})
		sink.Emit(event.TurnFinished{Reason: event.ReasonStop})
		return nil
	}}
	m := newProgModel(r)
	runProgram(t, m, func(send func(tea.Msg)) {
		send(keyEnter)
		// 等第一轮结束后再发第二条：用输入框内容区分两轮。
		time.Sleep(300 * time.Millisecond)
		send(typed("二"))
		send(keyEnter)
	}, func(m *Model) bool { return m.turn >= 2 && !m.running })

	s := plain(m)
	mustContain(t, s, "回答:看一下目录", "回答:二")
	if n.Load() != 2 {
		t.Errorf("Runner 应被调用 2 次，实际 %d", n.Load())
	}
}
