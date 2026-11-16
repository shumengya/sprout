package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shumengya/sprout/internal/agent/event"
	"github.com/shumengya/sprout/internal/agent/tool"
)

// parallelAgent 构造一个带多个工具、关闭重试的 agent。
func parallelAgent(t *testing.T, p Provider, limit int, tools ...tool.Tool) *Agent {
	t.Helper()
	return newAgentCfg(t, Config{Provider: p, Tools: registry(t, tools...), Retry: &RetryPolicy{}, MaxParallelTools: limit})
}

// barrierTool 到达后等待 n 个调用都到齐才返回。用栅栏证明“真的同时在跑”，不靠 sleep 估时间：
// 如果工具是串行执行的，第一个调用永远等不到第二个，只能靠超时失败。
func barrierTool(t *testing.T, name string, n int) tool.Tool {
	var wg sync.WaitGroup
	wg.Add(n)
	return fakeTool{name: name, run: func(ctx context.Context, _ json.RawMessage) (tool.Result, error) {
		wg.Done()
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
			return tool.Result{Command: name, Stdout: "同时到达"}, nil
		case <-time.After(5 * time.Second):
			t.Error("工具没有并发执行：栅栏等待超时")
			return tool.Result{}, errors.New("timeout")
		}
	}}
}

func TestToolsRunConcurrently(t *testing.T) {
	p := &scripted{steps: []scriptStep{
		callTools(call("1", "gate", `{}`), call("2", "gate", `{}`), call("3", "gate", `{}`)),
		say("好"),
	}}
	a := parallelAgent(t, p, 0, barrierTool(t, "gate", 3))

	start := time.Now()
	if err := a.Run(context.Background(), Input{Text: "q"}, &collector{}); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatal("耗时过长，工具可能是串行执行的")
	}
}

func TestResultsKeepCallOrderRegardlessOfCompletionOrder(t *testing.T) {
	// 第 1 个调用最慢、第 3 个最快：完成顺序是 3,2,1，历史里仍必须是 1,2,3，
	// 因为 tool 消息要和 assistant 的 tool_calls 一一对应。
	// 不能在工具里直接放行下一个：那时完成消息还没发给主 goroutine，事件先后不确定（之前就因此偶发失败）。
	// 改成等前一个调用的 ToolFinished 事件真正被 sink 收到，顺序才是确定的。
	emitted := map[string]chan struct{}{"2": make(chan struct{}), "3": make(chan struct{})}
	mk := func(name, waitFor string) tool.Tool {
		return fakeTool{name: name, run: func(context.Context, json.RawMessage) (tool.Result, error) {
			if waitFor != "" {
				select {
				case <-emitted[waitFor]:
				case <-time.After(5 * time.Second):
					t.Errorf("%s 等待 %s 的完成事件超时", name, waitFor)
				}
			}
			return tool.Result{Command: name, Stdout: name}, nil
		}}
	}
	p := &scripted{steps: []scriptStep{
		callTools(call("1", "t1", `{}`), call("2", "t2", `{}`), call("3", "t3", `{}`)),
		say("好"),
	}}
	a := parallelAgent(t, p, 0, mk("t1", "2"), mk("t2", "3"), mk("t3", ""))
	var c collector
	gate := event.SinkFunc(func(e event.Event) {
		if f, ok := e.(event.ToolFinished); ok {
			if ch := emitted[f.ID]; ch != nil {
				close(ch)
			}
		}
	})
	if err := a.Run(context.Background(), Input{Text: "q"}, event.Multi(&c, gate)); err != nil {
		t.Fatal(err)
	}

	h := a.History()
	if len(h) != 6 || h[2].ToolID != "1" || h[3].ToolID != "2" || h[4].ToolID != "3" {
		t.Fatalf("历史必须按调用顺序: %+v", h)
	}
	var finished []string
	for _, e := range c.events {
		if f, ok := e.(event.ToolFinished); ok {
			finished = append(finished, f.ID)
		}
	}
	if !slices.Equal(finished, []string{"3", "2", "1"}) {
		t.Errorf("ToolFinished 应按完成顺序发出，得到 %v", finished)
	}
}

func TestRequestedEventsFollowCallOrder(t *testing.T) {
	p := &scripted{steps: []scriptStep{
		callTools(call("1", "a", `{}`), call("2", "a", `{}`), call("3", "a", `{}`)),
		say("好"),
	}}
	a := parallelAgent(t, p, 0, okTool("a", tool.Result{}))
	var c collector
	if err := a.Run(context.Background(), Input{Text: "q"}, &c); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range c.events {
		if r, ok := e.(event.ToolRequested); ok {
			ids = append(ids, r.Call.ID)
		}
	}
	if !slices.Equal(ids, []string{"1", "2", "3"}) {
		t.Errorf("ToolRequested 应按调用顺序，得到 %v", ids)
	}
}

// gauge 记录同时在执行的工具数峰值。
type gauge struct{ cur, peak atomic.Int32 }

func (g *gauge) tool(name string, hold time.Duration) tool.Tool {
	return fakeTool{name: name, run: func(context.Context, json.RawMessage) (tool.Result, error) {
		n := g.cur.Add(1)
		for {
			p := g.peak.Load()
			if n <= p || g.peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(hold)
		g.cur.Add(-1)
		return tool.Result{Command: name}, nil
	}}
}

func fiveCalls() scriptStep {
	var cs []ToolCall
	for _, id := range []string{"1", "2", "3", "4", "5"} {
		cs = append(cs, call(id, "w", `{}`))
	}
	return callTools(cs...)
}

func TestConcurrencyIsCapped(t *testing.T) {
	for _, limit := range []int{1, 2, 3} {
		var g gauge
		p := &scripted{steps: []scriptStep{fiveCalls(), say("好")}}
		a := parallelAgent(t, p, limit, g.tool("w", 30*time.Millisecond))
		if err := a.Run(context.Background(), Input{Text: "q"}, &collector{}); err != nil {
			t.Fatal(err)
		}
		if got := int(g.peak.Load()); got != limit {
			t.Errorf("limit=%d: 同时执行的峰值是 %d，应恰好等于上限", limit, got)
		}
		if h := a.History(); len(h) != 8 {
			t.Errorf("limit=%d: 5 个调用的结果都应在历史里: %d 条", limit, len(h))
		}
	}
}

func TestDefaultLimitIsFour(t *testing.T) {
	var g gauge
	p := &scripted{steps: []scriptStep{fiveCalls(), say("好")}}
	a := parallelAgent(t, p, 0, g.tool("w", 40*time.Millisecond))
	if err := a.Run(context.Background(), Input{Text: "q"}, &collector{}); err != nil {
		t.Fatal(err)
	}
	if got := int(g.peak.Load()); got != DefaultMaxParallelTools {
		t.Errorf("默认并发上限应为 %d，峰值 %d", DefaultMaxParallelTools, got)
	}
}

func TestSerialModeKeepsRequestedFinishedInterleaved(t *testing.T) {
	// 串行时第 2 个调用在第 1 个结束后才“开始”，界面上不能同时显示两个在运行。
	p := &scripted{steps: []scriptStep{callTools(call("1", "w", `{}`), call("2", "w", `{}`)), say("好")}}
	a := parallelAgent(t, p, 1, okTool("w", tool.Result{}))
	var c collector
	if err := a.Run(context.Background(), Input{Text: "q"}, &c); err != nil {
		t.Fatal(err)
	}
	var seq []event.Kind
	for _, k := range c.kinds() {
		if k == event.KindToolRequested || k == event.KindToolFinished {
			seq = append(seq, k)
		}
	}
	want := []event.Kind{event.KindToolRequested, event.KindToolFinished, event.KindToolRequested, event.KindToolFinished}
	if !slices.Equal(seq, want) {
		t.Fatalf("串行时事件应交替出现: %v", seq)
	}
}

func TestCancelAbortsAllRunningTools(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var running atomic.Int32
	blocker := fakeTool{name: "w", run: func(ctx context.Context, _ json.RawMessage) (tool.Result, error) {
		if running.Add(1) == 2 {
			cancel() // 两个都已经在跑时取消
		}
		<-ctx.Done()
		return tool.Result{Command: "sleep"}, ctx.Err()
	}}
	p := &scripted{steps: []scriptStep{callTools(call("1", "w", `{}`), call("2", "w", `{}`)), say("不该到这")}}
	a := parallelAgent(t, p, 0, blocker)
	var c collector

	if err := a.Run(ctx, Input{Text: "q"}, &c); !errors.Is(err, context.Canceled) {
		t.Fatalf("得到 %v", err)
	}
	if c.finishedReason(t) != event.ReasonCancelled || len(p.reqs) != 1 {
		t.Fatalf("取消后不应再请求模型: reason=%q 请求 %d 次", c.finishedReason(t), len(p.reqs))
	}
	h := a.History()
	// 最后一条是中断说明：这一轮保留在历史里，模型知道发生了什么。
	if len(h) != 5 || h[2].ToolID != "1" || h[3].ToolID != "2" || !strings.Contains(h[4].Text(), "中断") {
		t.Fatalf("两个调用都应有对应的 tool 消息，并附中断说明: %+v", h)
	}
}

func TestQueuedCallsAreSkippedWhenCancelled(t *testing.T) {
	// 并发上限 2，共 4 个调用：前两个在跑时取消，排队的两个不应该再开始。
	ctx, cancel := context.WithCancel(context.Background())
	var executed atomic.Int32
	w := fakeTool{name: "w", run: func(ctx context.Context, _ json.RawMessage) (tool.Result, error) {
		if executed.Add(1) == 2 {
			cancel()
		}
		<-ctx.Done()
		return tool.Result{}, ctx.Err()
	}}
	p := &scripted{steps: []scriptStep{
		callTools(call("1", "w", `{}`), call("2", "w", `{}`), call("3", "w", `{}`), call("4", "w", `{}`)),
	}}
	a := parallelAgent(t, p, 2, w)
	var c collector
	if err := a.Run(ctx, Input{Text: "q"}, &c); !errors.Is(err, context.Canceled) {
		t.Fatalf("得到 %v", err)
	}
	if executed.Load() != 2 {
		t.Fatalf("排队的调用不应开始执行，实际执行了 %d 个", executed.Load())
	}
	h := a.History()
	if len(h) != 7 || h[4].Text() != cancelledToolReply || h[5].Text() != cancelledToolReply || !strings.Contains(h[6].Text(), "中断") {
		t.Fatalf("排队的两个应补“未执行”，末尾附中断说明: %+v", h)
	}
}

func TestOneFailingToolDoesNotAffectOthers(t *testing.T) {
	bad := fakeTool{name: "bad", run: func(context.Context, json.RawMessage) (tool.Result, error) {
		return tool.Result{}, errors.New("起不来")
	}}
	good := okTool("good", tool.Result{Command: "ok", Stdout: "成功"})
	p := &scripted{steps: []scriptStep{callTools(call("1", "bad", `{}`), call("2", "good", `{}`)), say("好")}}
	a := parallelAgent(t, p, 0, bad, good)
	if err := a.Run(context.Background(), Input{Text: "q"}, &collector{}); err != nil {
		t.Fatal(err)
	}
	h := a.History()
	if h[2].Text() != "工具执行失败: 起不来" || h[3].Text() == "" {
		t.Fatalf("历史 = %+v", h)
	}
}

func TestSystemPromptTellsModelAboutParallelCalls(t *testing.T) {
	p := promptFor("bash")
	for _, want := range []string{"并行执行", "彼此没有先后顺序", "分到不同的回合"} {
		if !strings.Contains(p, want) {
			t.Errorf("提示词缺少 %q", want)
		}
	}
	// 有 shell 时给出“合并成一条命令”这个更省事的选择；没有 shell 时不该提它。
	if !strings.Contains(p, "shell 命令") {
		t.Error("有 shell 工具时应提示把有依赖的步骤合并成一条 shell 命令")
	}
	if q := promptFor("read", "edit"); strings.Contains(q, "合并成一条") || strings.Contains(q, "shell 命令") {
		t.Errorf("没有 shell 工具时不应提合并命令:\n%s", q)
	}
}

// serialTool 把工具声明成不可并行，模拟 write / edit / delete。
type serialTool struct{ tool.Tool }

func (serialTool) Parallel() bool { return false }

// timeline 按发生顺序记录每个调用的开始和结束，参数里的 id 区分调用。
type timeline struct {
	mu  sync.Mutex
	log []string
}

func (tl *timeline) tool(name string, hold time.Duration) tool.Tool {
	return fakeTool{name: name, run: func(_ context.Context, args json.RawMessage) (tool.Result, error) {
		var a struct{ ID string }
		_ = json.Unmarshal(args, &a)
		tl.add("start:" + a.ID)
		time.Sleep(hold)
		tl.add("end:" + a.ID)
		return tool.Result{Command: name}, nil
	}}
}

func (tl *timeline) add(s string) {
	tl.mu.Lock()
	tl.log = append(tl.log, s)
	tl.mu.Unlock()
}

func (tl *timeline) at(t *testing.T, s string) int {
	t.Helper()
	i := slices.Index(tl.log, s)
	if i < 0 {
		t.Fatalf("时间线里没有 %s: %v", s, tl.log)
	}
	return i
}

func TestSerialToolRunsAlone(t *testing.T) {
	var tl timeline
	p := &scripted{steps: []scriptStep{callTools(
		call("1", "p", `{"id":"1"}`), call("2", "p", `{"id":"2"}`),
		call("3", "s", `{"id":"3"}`),
		call("4", "p", `{"id":"4"}`), call("5", "p", `{"id":"5"}`),
	), say("好")}}
	a := parallelAgent(t, p, 0, tl.tool("p", 30*time.Millisecond), serialTool{tl.tool("s", 30*time.Millisecond)})
	if err := a.Run(context.Background(), Input{Text: "q"}, &collector{}); err != nil {
		t.Fatal(err)
	}
	s, e := tl.at(t, "start:3"), tl.at(t, "end:3")
	if e != s+1 {
		t.Fatalf("串行调用执行期间不应有别的调用开始或结束: %v", tl.log)
	}
	for _, id := range []string{"1", "2"} {
		if tl.at(t, "end:"+id) > s {
			t.Errorf("前面的调用 %s 应在串行调用开始前结束: %v", id, tl.log)
		}
	}
	for _, id := range []string{"4", "5"} {
		if tl.at(t, "start:"+id) < e {
			t.Errorf("后面的调用 %s 应在串行调用结束后才开始: %v", id, tl.log)
		}
	}
	// 串行调用前后的可并行调用仍然并发：两个都开始了才有人结束。
	if tl.at(t, "start:2") > tl.at(t, "end:1") && tl.at(t, "start:1") > tl.at(t, "end:2") {
		t.Errorf("1 和 2 应并发执行: %v", tl.log)
	}
	if tl.at(t, "start:5") > tl.at(t, "end:4") && tl.at(t, "start:4") > tl.at(t, "end:5") {
		t.Errorf("4 和 5 应并发执行: %v", tl.log)
	}
	h := a.History()
	for i, id := range []string{"1", "2", "3", "4", "5"} {
		if h[2+i].ToolID != id {
			t.Fatalf("历史必须按调用顺序: %+v", h)
		}
	}
}

func TestSerialToolSkippedWhenCancelledWhileWaiting(t *testing.T) {
	// 串行调用在等前面的调用让出名额时本轮被取消：它不该开始，也要有“未执行”的结果。
	ctx, cancel := context.WithCancel(context.Background())
	blocker := fakeTool{name: "p", run: func(ctx context.Context, _ json.RawMessage) (tool.Result, error) {
		cancel()
		<-ctx.Done()
		return tool.Result{Command: "sleep"}, ctx.Err()
	}}
	var ran atomic.Bool
	s := serialTool{fakeTool{name: "s", run: func(context.Context, json.RawMessage) (tool.Result, error) {
		ran.Store(true)
		return tool.Result{}, nil
	}}}
	p := &scripted{steps: []scriptStep{callTools(call("1", "p", `{}`), call("2", "s", `{}`)), say("不该到这")}}
	a := parallelAgent(t, p, 0, blocker, s)
	var c collector
	if err := a.Run(ctx, Input{Text: "q"}, &c); !errors.Is(err, context.Canceled) {
		t.Fatalf("得到 %v", err)
	}
	if ran.Load() {
		t.Fatal("取消后串行调用不应再执行")
	}
	for _, e := range c.events {
		if r, ok := e.(event.ToolRequested); ok && r.Call.ID == "2" {
			t.Fatal("没执行的调用不应发 ToolRequested")
		}
	}
	h := a.History()
	if len(h) < 4 || h[2].ToolID != "1" || h[3].ToolID != "2" || h[3].Text() != cancelledToolReply {
		t.Fatalf("两个调用都应有对应的 tool 消息: %+v", h)
	}
}
