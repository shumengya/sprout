package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/event"
)

// fakeCompactor 是可编程的手动压缩替身。只用于测试。
type fakeCompactor struct {
	events []event.Event // 压缩时按顺序发出的事件
	err    error
	block  bool // 阻塞到 ctx 被取消，模拟模型还没返回
	boom   bool // 直接 panic
	calls  int
	focus  string
}

func (f *fakeCompactor) Compact(ctx context.Context, focus string, sink event.Sink) error {
	f.calls++
	f.focus = focus
	if f.boom {
		panic("压缩炸了")
	}
	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	for _, e := range f.events {
		sink.Emit(e)
	}
	return f.err
}

func compactModel(t *testing.T, c *fakeCompactor) *Model {
	t.Helper()
	return newSized(t, Options{
		Runner: scriptRunner(), Compactor: c, Models: testModels(), Sessions: &fakeSessions{},
	}, 120, 40)
}

// startedCompact 提交一条命令，并拆开 startCompact 返回的批量命令：run 是真正干活的压缩，
// tick 是转圈动画。真实运行时两者并发，测试里由调用方决定何时执行 run。
func startedCompact(t *testing.T, m *Model, text string) (run, tick tea.Cmd) {
	t.Helper()
	m.in.SetValue(text)
	_, cmd := m.Update(keyEnter)
	if cmd == nil {
		t.Fatalf("%q 应返回异步命令", text)
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("应返回 [压缩, 动画] 两个并发命令，得到 %T", cmd())
	}
	return batch[0], batch[1]
}

func waitMsg(t *testing.T, ch <-chan tea.Msg) tea.Msg {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(3 * time.Second):
		t.Fatal("等待压缩结束超时")
		return nil
	}
}

// goRun 在另一个 goroutine 里执行 run，结果从返回的通道取。用于会阻塞的压缩。
func goRun(run tea.Cmd) <-chan tea.Msg {
	ch := make(chan tea.Msg, 1)
	go func() { ch <- run() }()
	return ch
}

func TestCompactRunsAndReportsTheResult(t *testing.T) {
	c := &fakeCompactor{events: []event.Event{
		event.Compacted{Mode: event.CompactSummary, Manual: true, Dropped: 12, TokensBefore: 9000, TokensAfter: 1200, Summary: "摘要"},
	}}
	m := compactModel(t, c)
	// 压缩开始时记下时刻，结束前把时钟拨快 7 秒：耗时应只记这段，空闲时不涨。
	start := m.now()
	clock := start
	m.now = func() time.Time { return clock }

	run, _ := startedCompact(t, m, "/compact")
	clock = start.Add(7 * time.Second)
	if !m.compacting || m.running {
		t.Fatalf("应进入压缩中而不是生成中: compacting=%v running=%v", m.compacting, m.running)
	}
	mustContain(t, plain(m), "压缩上下文", "Ctrl+C 取消")

	m.Update(run())
	if m.compacting {
		t.Fatal("压缩结束后应回到空闲")
	}
	s := plain(m)
	mustContain(t, s, "已把前面 12 条消息总结成摘要", "约 9k → 1.2k token")
	mustNotContain(t, s, "上下文接近上限", "Ctrl+C 取消")

	// 压缩也算进耗时：压缩期间随时钟走，结束后定格，空闲时不再涨。
	if line := infoText(m, 0); !strings.Contains(line, "耗时 7s") {
		t.Fatalf("压缩结束后耗时应定格: %q", line)
	}
	clock = start.Add(time.Minute)
	if line := infoText(m, 0); !strings.Contains(line, "耗时 7s") {
		t.Errorf("空闲后耗时不应再涨: %q", line)
	}

	// 结束后可以正常继续对话，没有被卡住。
	m.in.SetValue("接着聊")
	if _, cmd := m.Update(keyEnter); cmd == nil || !m.running {
		t.Fatal("压缩结束后应能继续发送消息")
	}
}

func TestCompactPassesTheUserFocus(t *testing.T) {
	c := &fakeCompactor{}
	m := compactModel(t, c)
	run, _ := startedCompact(t, m, "/compact   保留文件路径和报错  ")
	m.Update(run())
	if c.focus != "保留文件路径和报错" {
		t.Errorf("focus = %q，应去掉首尾空白后原样传给内核", c.focus)
	}

	c2 := &fakeCompactor{}
	m2 := compactModel(t, c2)
	run, _ = startedCompact(t, m2, "/compact")
	m2.Update(run())
	if c2.calls != 1 || c2.focus != "" {
		t.Errorf("不带说明时 focus 应为空: calls=%d focus=%q", c2.calls, c2.focus)
	}
}

func TestCompactBlocksOtherActionsWhileRunning(t *testing.T) {
	c := &fakeCompactor{block: true}
	m := compactModel(t, c)
	run, _ := startedCompact(t, m, "/compact")
	done := goRun(run)
	turn := m.turn

	submit(m, "你好")
	mustContain(t, plain(m), "正在压缩上下文，请稍候")
	if m.running || m.turn != turn {
		t.Error("压缩期间发送消息不应启动新的一轮")
	}
	for cmd, want := range map[string]string{
		"/model":   "暂时不能切换模型",
		"/new":     "暂时不能新建会话",
		"/resume":  "暂时不能切换会话",
		"/compact": "暂时不能压缩上下文",
	} {
		submit(m, cmd)
		mustContain(t, plain(m), want)
	}
	if m.picker != nil || m.browser != nil {
		t.Error("压缩期间不该打开模型选择器或会话浏览器")
	}

	m.Update(keyCtrlC) // 收尾：取消并等 goroutine 结束
	m.Update(waitMsg(t, done))
	if c.calls != 1 {
		t.Errorf("被拒绝的 /compact 不该再调用内核: calls=%d", c.calls)
	}
}

func TestCompactRefusedWhileGenerating(t *testing.T) {
	c := &fakeCompactor{}
	m := compactModel(t, c)
	begin(m, "q") // 正在生成
	submit(m, "/compact")
	mustContain(t, plain(m), "暂时不能压缩上下文")
	if m.compacting || c.calls != 0 {
		t.Errorf("生成中不能压缩: compacting=%v calls=%d", m.compacting, c.calls)
	}
}

func TestCtrlCCancelsCompactionWithoutQuitting(t *testing.T) {
	c := &fakeCompactor{block: true}
	m := compactModel(t, c)
	run, _ := startedCompact(t, m, "/compact")
	done := goRun(run)

	_, cmd := m.Update(keyCtrlC)
	if isQuit(cmd) {
		t.Fatal("压缩中按 Ctrl+C 只应取消压缩，不应退出程序")
	}
	mustContain(t, plain(m), "正在取消")

	m.Update(waitMsg(t, done))
	if m.compacting {
		t.Fatal("取消完成后应回到空闲")
	}
	mustContain(t, plain(m), "已取消压缩，对话保持原样")
	mustNotContain(t, plain(m), "已把前面", "压缩上下文失败")
}

func TestCompactNothingToCompact(t *testing.T) {
	m := compactModel(t, &fakeCompactor{err: agent.ErrNothingToCompact})
	run, _ := startedCompact(t, m, "/compact")
	m.Update(run())
	mustContain(t, plain(m), "还没有可压缩的对话")
	if m.compacting {
		t.Error("应回到空闲")
	}
}

func TestCompactFailureIsShownOnlyOnce(t *testing.T) {
	// 摘要失败时内核既发了带 Err 的事件、又返回了同一个错误；界面只该提示一次。
	c := &fakeCompactor{
		events: []event.Event{event.Compacted{Mode: event.CompactSummary, Manual: true, TokensBefore: 8000, TokensAfter: 8000, Err: "模型返回了空摘要"}},
		err:    errors.New("模型返回了空摘要"),
	}
	m := compactModel(t, c)
	run, _ := startedCompact(t, m, "/compact")
	m.Update(run())
	s := plain(m)
	mustContain(t, s, "压缩上下文失败，对话保持原样：模型返回了空摘要")
	if n := strings.Count(s, "模型返回了空摘要"); n != 1 {
		t.Errorf("失败原因应只出现一次，实际 %d 次:\n%s", n, s)
	}
}

func TestCompactErrorWithoutEventStillShowsAMessage(t *testing.T) {
	m := compactModel(t, &fakeCompactor{err: errors.New("网络不通")})
	run, _ := startedCompact(t, m, "/compact")
	m.Update(run())
	mustContain(t, plain(m), "压缩上下文失败：网络不通")
}

func TestCompactUnavailableWithoutACompactor(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 30)
	submit(m, "/compact")
	mustContain(t, plain(m), "当前不能压缩上下文")
	if m.compacting {
		t.Error("没有 Compactor 时不该进入压缩中")
	}
}

func TestCompactPanicDoesNotWedgeTheUI(t *testing.T) {
	m := compactModel(t, &fakeCompactor{boom: true})
	run, _ := startedCompact(t, m, "/compact")
	msg := run() // 不能把 panic 传出去，否则整个程序崩掉
	cm, ok := msg.(compactDoneMsg)
	if !ok || cm.err == nil {
		t.Fatalf("panic 应转成带错误的结果消息: %+v", msg)
	}
	m.Update(msg)
	if m.compacting {
		t.Fatal("panic 之后界面不能一直停在压缩中")
	}
	mustContain(t, plain(m), "压缩上下文失败：内部错误: 压缩炸了")
	select {
	case <-m.finished:
	default:
		t.Error("panic 之后也要通知 Shutdown 可以退出了")
	}
}

func TestShutdownCancelsAndWaitsForCompaction(t *testing.T) {
	c := &fakeCompactor{block: true}
	m := compactModel(t, c)
	run, _ := startedCompact(t, m, "/compact")
	done := goRun(run)

	start := time.Now()
	m.Shutdown(3 * time.Second)
	if time.Since(start) >= 3*time.Second {
		t.Fatal("Shutdown 应在压缩被取消后立刻返回，而不是等到超时")
	}
	cm, ok := waitMsg(t, done).(compactDoneMsg)
	if !ok || !errors.Is(cm.err, context.Canceled) {
		t.Errorf("退出时应取消正在进行的压缩: %+v", cm)
	}
}

func TestCompactKeepsTheSpinnerTickingUntilDone(t *testing.T) {
	m := compactModel(t, &fakeCompactor{})
	run, tick := startedCompact(t, m, "/compact")
	msg := tick()
	if _, cmd := m.Update(msg); cmd == nil {
		t.Fatal("压缩期间转圈动画应继续")
	}
	m.Update(run())
	if _, cmd := m.Update(msg); cmd != nil {
		t.Fatal("压缩结束后动画应停下，不能留下空转的定时器")
	}
}

func TestManualCompactedWordingIsNotTheAutomaticOne(t *testing.T) {
	events := []event.Event{
		event.Compacted{Mode: event.CompactSummary, Manual: true, Dropped: 12, TokensBefore: 8000, TokensAfter: 2500},
		event.Compacted{Mode: event.CompactSummary, Manual: true, TokensBefore: 8000, Err: "模型返回了空摘要"},
	}
	want := []string{
		"已把前面 12 条消息总结成摘要", "约 8k → 2.5k token",
		"压缩上下文失败，对话保持原样：模型返回了空摘要",
	}

	live := newSized(t, Options{Runner: scriptRunner()}, 120, 40)
	begin(live, "q")
	apply(live, events...)
	mustContain(t, plain(live), want...)
	mustNotContain(t, plain(live), "上下文接近上限")

	// 恢复会话时走重放：同样的事件要得到同样的措辞。
	resumed := newSized(t, Options{Runner: scriptRunner()}, 120, 40)
	resumed.replay(events)
	resumed.refresh()
	mustContain(t, plain(resumed), want...)
	mustNotContain(t, plain(resumed), "上下文接近上限")
}

func TestCompactIsListedInSuggestionsAndHelp(t *testing.T) {
	m := compactModel(t, &fakeCompactor{})
	typeText(m, "/comp")
	if got := sugLabels(m); !slices.Equal(got, []string{"/compact"}) {
		t.Fatalf("候选 = %v", got)
	}

	h := compactModel(t, &fakeCompactor{})
	submit(h, "/help")
	mustContain(t, plain(h), "/compact")
}
