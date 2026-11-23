package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shumengya/sprout/internal/agent/event"
	"github.com/shumengya/sprout/internal/agent/tool"
)

// bgTool 是声明可后台执行的工具替身。只用于测试。
type bgTool struct{ fakeTool }

func (bgTool) Background() bool { return true }

func (b bgTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"zeta":{"type":"string"},"alpha":{"type":"string"}}}`)
}

// blockingBG 返回一个后台工具：先转发一个进度事件，然后等 release 关闭（或 ctx 取消）才结束。
// args 记下工具实际收到的参数。等了 5 秒还没被放行说明循环被它挡住了（release 要靠后续的模型请求关闭）。
func blockingBG(t *testing.T, name string, release <-chan struct{}, args *[]string, mu *sync.Mutex) bgTool {
	return bgTool{fakeTool{name: name, run: func(ctx context.Context, a json.RawMessage) (tool.Result, error) {
		mu.Lock()
		*args = append(*args, string(a))
		mu.Unlock()
		if c, ok := tool.CallFrom(ctx); ok {
			c.Emit(event.Subagent{CallID: c.ID, Agent: "x", Event: event.Usage{OutputTokens: 1}})
		}
		select {
		case <-release:
			return tool.Result{Command: name, Stdout: "后台结果"}, nil
		case <-ctx.Done():
			return tool.Result{Command: name, Stdout: "做了一半"}, ctx.Err()
		case <-time.After(5 * time.Second):
			t.Error("后台调用一直没被放行：ReAct 循环可能被它挡住了")
			return tool.Result{}, errors.New("timeout")
		}
	}}}
}

func roles(h []Message) []Role {
	var out []Role
	for _, m := range h {
		out = append(out, m.Role)
	}
	return out
}

func indexOf(c *collector, match func(event.Event) bool) int {
	return slices.IndexFunc(c.events, match)
}

// 要求后台执行的调用当场返回占位结果，ReAct 循环接着请求模型；模型给出答案时还没结束，
// 这一轮就等它，结果作为用户消息送达后再请求一次模型。
func TestBackgroundCallDoesNotBlockLoop(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	var got []string
	slow := blockingBG(t, "slow", release, &got, &mu)

	// 后台调用要等第二次请求模型时才放行：循环要是被它挡住，就到不了第二次请求。
	second := say("先等后台")
	second.hook = func(context.Context) { close(release) }
	p := &scripted{steps: []scriptStep{
		callTools(call("1", "slow", `{"zeta":"z","run_in_background":true}`)),
		second,
		say("汇总"),
	}}
	ag := newAgent(t, p, registry(t, slow, okTool("plain", tool.Result{})), nil)
	var c collector
	if err := ag.Run(context.Background(), Input{Text: "q"}, &c); err != nil {
		t.Fatal(err)
	}
	if r := c.finishedReason(t); r != event.ReasonStop {
		t.Fatalf("结束原因 = %s", r)
	}
	if len(got) != 1 || got[0] != `{"zeta":"z"}` {
		t.Errorf("工具收到的参数应去掉 %s：%v", tool.BackgroundParam, got)
	}

	placeholder := indexOf(&c, func(e event.Event) bool {
		f, ok := e.(event.ToolFinished)
		return ok && f.ID == "1" && f.Background && strings.Contains(f.Content, "任务编号 1")
	})
	progress := indexOf(&c, func(e event.Event) bool { _, ok := e.(event.Subagent); return ok })
	done := indexOf(&c, func(e event.Event) bool { _, ok := e.(event.BackgroundFinished); return ok })
	if placeholder < 0 || progress < placeholder || done < progress {
		t.Fatalf("事件顺序应为 占位 tool.finished → 进度 → background.finished：%v", c.kinds())
	}
	bf := c.events[done].(event.BackgroundFinished)
	if bf.Result.ID != "1" || bf.Result.Stdout != "后台结果" || bf.Result.Background || !strings.Contains(bf.Message, bf.Result.Content) {
		t.Errorf("BackgroundFinished 不对：%+v", bf)
	}

	h := ag.History()
	if want := []Role{RoleUser, RoleAssistant, RoleTool, RoleAssistant, RoleUser, RoleAssistant}; !slices.Equal(roles(h), want) {
		t.Fatalf("历史 = %v，期望 %v", roles(h), want)
	}
	if !strings.Contains(h[2].Text(), "已转入后台") || h[4].Text() != bf.Message {
		t.Errorf("占位结果或送达消息不对：%q / %q", h[2].Text(), h[4].Text())
	}
	if last := p.reqs[2].Messages; last[len(last)-1].Text() != bf.Message {
		t.Errorf("第三次请求应以后台结果结尾")
	}

	// 只有可后台的工具多出开关参数，原有参数的顺序保持不变。
	for _, spec := range p.reqs[0].Tools {
		has := strings.Contains(string(spec.Parameters), tool.BackgroundParam)
		if has != (spec.Name == "slow") {
			t.Errorf("%s 的参数里有没有 %s：%v", spec.Name, tool.BackgroundParam, has)
		}
		if spec.Name == "slow" {
			s := string(spec.Parameters)
			if !strings.Contains(s, "一般不要用") {
				t.Errorf("开关的描述应要求模型默认不用：%s", s)
			}
			if !(strings.Index(s, "zeta") < strings.Index(s, "alpha") && strings.Index(s, "alpha") < strings.Index(s, tool.BackgroundParam)) {
				t.Errorf("参数顺序被打乱：%s", s)
			}
		}
	}
}

// 后台调用在下一批前台工具执行期间结束：结果在这批工具结果之后、下一次请求模型之前送达。
func TestBackgroundResultDeliveredAfterNextBatch(t *testing.T) {
	returned := make(chan struct{})
	quick := bgTool{fakeTool{name: "quick", run: func(context.Context, json.RawMessage) (tool.Result, error) {
		defer close(returned)
		return tool.Result{Command: "quick", Stdout: "很快"}, nil
	}}}
	fg := fakeTool{name: "fg", run: func(context.Context, json.RawMessage) (tool.Result, error) {
		<-returned
		time.Sleep(50 * time.Millisecond) // 等后台调用把结果交给内核（工具返回到入队之间只差几条语句）
		return tool.Result{Command: "fg"}, nil
	}}
	p := &scripted{steps: []scriptStep{
		callTools(call("1", "quick", `{"run_in_background":true}`)),
		callTools(call("2", "fg", `{}`)),
		say("好"),
	}}
	ag := newAgent(t, p, registry(t, quick, fg), nil)
	var c collector
	if err := ag.Run(context.Background(), Input{Text: "q"}, &c); err != nil {
		t.Fatal(err)
	}
	fgDone := indexOf(&c, func(e event.Event) bool { f, ok := e.(event.ToolFinished); return ok && f.ID == "2" })
	bgDone := indexOf(&c, func(e event.Event) bool { _, ok := e.(event.BackgroundFinished); return ok })
	if fgDone < 0 || bgDone < fgDone {
		t.Fatalf("后台结果应在这批工具结果之后送达：%v", c.kinds())
	}
	req := p.reqs[2].Messages
	if n := len(req); n < 2 || req[n-2].Role != RoleTool || req[n-1].Role != RoleUser || !strings.Contains(req[n-1].Text(), "很快") {
		t.Fatalf("第三次请求应以 fg 的结果和后台结果结尾：%+v", req)
	}
}

// 等后台调用时取消：后台调用跟着取消，结果（被取消）先送进历史，再写中断说明，历史里不会有两条连续的 assistant。
func TestCancelWhileWaitingForBackground(t *testing.T) {
	var mu sync.Mutex
	var got []string
	slow := blockingBG(t, "slow", nil, &got, &mu)
	p := &scripted{steps: []scriptStep{
		callTools(call("1", "slow", `{"run_in_background":true}`)),
		say("等着"),
	}}
	ag := newAgent(t, p, registry(t, slow), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var c collector
	sink := event.SinkFunc(func(e event.Event) {
		c.Emit(e)
		if m, ok := e.(event.MessageCompleted); ok && len(m.ToolCalls) == 0 {
			cancel()
		}
	})
	if err := ag.Run(ctx, Input{Text: "q"}, sink); !errors.Is(err, context.Canceled) {
		t.Fatalf("应返回取消错误，得到 %v", err)
	}
	if r := c.finishedReason(t); r != event.ReasonCancelled {
		t.Fatalf("结束原因 = %s", r)
	}
	done := indexOf(&c, func(e event.Event) bool { _, ok := e.(event.BackgroundFinished); return ok })
	intr := indexOf(&c, func(e event.Event) bool { _, ok := e.(event.Interrupted); return ok })
	if done < 0 || intr < done {
		t.Fatalf("应先送达被取消的后台结果，再发 interrupted：%v", c.kinds())
	}
	bf := c.events[done].(event.BackgroundFinished)
	if !bf.Result.Cancelled || !strings.Contains(bf.Message, "被用户取消") || !strings.Contains(bf.Message, "做了一半") {
		t.Errorf("被取消的后台结果不对：%+v", bf)
	}
	if want := []Role{RoleUser, RoleAssistant, RoleTool, RoleAssistant, RoleUser, RoleAssistant}; !slices.Equal(roles(ag.History()), want) {
		t.Fatalf("历史 = %v，期望 %v", roles(ag.History()), want)
	}
}

// 等后台调用期间用户排了新输入：不必等后台结束，马上接进历史再请求模型。
func TestQueuedInputWakesBackgroundWait(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	var got []string
	slow := blockingBG(t, "slow", release, &got, &mu)

	var qmu sync.Mutex
	var queue []Input
	queued := func() []Input {
		qmu.Lock()
		defer qmu.Unlock()
		out := queue
		queue = nil
		return out
	}
	reply := say("收到补充")
	reply.hook = func(context.Context) { close(release) }
	p := &scripted{steps: []scriptStep{
		callTools(call("1", "slow", `{"run_in_background":true}`)),
		say("等着"),
		reply,
		say("汇总"),
	}}
	ag := newAgent(t, p, registry(t, slow), nil)
	var c collector
	sink := event.SinkFunc(func(e event.Event) {
		c.Emit(e)
		if m, ok := e.(event.MessageCompleted); ok && m.Content == "等着" {
			qmu.Lock()
			queue = append(queue, Input{Text: "补充一句"})
			qmu.Unlock()
		}
	})
	if err := ag.Run(context.Background(), Input{Text: "q", Queued: queued}, sink); err != nil {
		t.Fatal(err)
	}
	uq := indexOf(&c, func(e event.Event) bool { _, ok := e.(event.UserQueued); return ok })
	bf := indexOf(&c, func(e event.Event) bool { _, ok := e.(event.BackgroundFinished); return ok })
	if uq < 0 || bf < uq {
		t.Fatalf("排队输入应先于后台结果送达：%v", c.kinds())
	}
	if len(p.reqs) != 4 {
		t.Fatalf("应请求模型 4 次，实际 %d 次", len(p.reqs))
	}
	if m := p.reqs[2].Messages; m[len(m)-1].Text() != "补充一句" {
		t.Errorf("第三次请求应以排队输入结尾")
	}
}

// 本轮出错时还在跑的后台调用被终止，结果说明是“终止”而不是“用户取消”。
func TestProviderErrorStopsBackground(t *testing.T) {
	var mu sync.Mutex
	var got []string
	slow := blockingBG(t, "slow", nil, &got, &mu)
	p := &scripted{steps: []scriptStep{
		callTools(call("1", "slow", `{"run_in_background":true}`)),
		{err: errors.New("服务端坏了")},
	}}
	ag := newAgent(t, p, registry(t, slow), nil)
	var c collector
	if err := ag.Run(context.Background(), Input{Text: "q"}, &c); err == nil {
		t.Fatal("应返回错误")
	}
	if r := c.finishedReason(t); r != event.ReasonError {
		t.Fatalf("结束原因 = %s", r)
	}
	done := indexOf(&c, func(e event.Event) bool { _, ok := e.(event.BackgroundFinished); return ok })
	intr := indexOf(&c, func(e event.Event) bool { _, ok := e.(event.Interrupted); return ok })
	if done < 0 || intr < done {
		t.Fatalf("后台结果应在 interrupted 之前：%v", c.kinds())
	}
	if bf := c.events[done].(event.BackgroundFinished); !strings.Contains(bf.Message, "工具执行被终止") {
		t.Errorf("终止说明不对：%q", bf.Message)
	}
	if want := []Role{RoleUser, RoleAssistant, RoleTool, RoleUser, RoleAssistant}; !slices.Equal(roles(ag.History()), want) {
		t.Fatalf("历史 = %v，期望 %v", roles(ag.History()), want)
	}
}

// 没要求后台时照常前台执行，开关参数同样不交给工具；没声明可后台的工具收到开关也照常前台执行、参数原样。
func TestBackgroundFlagOnlyForDeclaredTools(t *testing.T) {
	var gotBG, gotPlain string
	bg := bgTool{fakeTool{name: "bg", run: func(_ context.Context, a json.RawMessage) (tool.Result, error) {
		gotBG = string(a)
		return tool.Result{Command: "bg"}, nil
	}}}
	plain := fakeTool{name: "plain", run: func(_ context.Context, a json.RawMessage) (tool.Result, error) {
		gotPlain = string(a)
		return tool.Result{Command: "plain"}, nil
	}}
	p := &scripted{steps: []scriptStep{
		callTools(call("1", "bg", `{"zeta":"z","run_in_background":false}`), call("2", "plain", `{"run_in_background":true}`)),
		say("好"),
	}}
	ag := newAgent(t, p, registry(t, bg, plain), nil)
	var c collector
	if err := ag.Run(context.Background(), Input{Text: "q"}, &c); err != nil {
		t.Fatal(err)
	}
	if c.has(event.KindBackgroundFinished) {
		t.Fatal("不该有后台调用")
	}
	for _, e := range c.events {
		if f, ok := e.(event.ToolFinished); ok && f.Background {
			t.Errorf("%s 不该转入后台", f.ID)
		}
	}
	if gotBG != `{"zeta":"z"}` || gotPlain != `{"run_in_background":true}` {
		t.Errorf("参数不对：bg=%s plain=%s", gotBG, gotPlain)
	}
}

func TestWithBackgroundParam(t *testing.T) {
	cases := map[string]string{
		`{"type":"object"}`:                                      `{"properties":{"run_in_background":`,
		`{"type":"object","properties":{}}`:                      `{"properties":{"run_in_background":`,
		`{"type":"object","properties":{"b":{},"a":{}}}`:         `{"properties":{"b":{},"a":{},"run_in_background":`,
		`{"type":"object","properties":{"run_in_background":1}}`: `{"type":"object","properties":{"run_in_background":1}}`,
		`not json`: `not json`,
	}
	for in, prefix := range cases {
		got := string(withBackgroundParam(json.RawMessage(in)))
		if !strings.HasPrefix(got, prefix) {
			t.Errorf("withBackgroundParam(%s) = %s，期望以 %s 开头", in, got, prefix)
		}
		if json.Valid([]byte(in)) && !json.Valid([]byte(got)) {
			t.Errorf("结果不是合法 JSON：%s", got)
		}
	}
}

func TestSplitBackground(t *testing.T) {
	cases := []struct {
		in, out string
		bg      bool
	}{
		{`{"a":1,"run_in_background":true}`, `{"a":1}`, true},
		{`{"run_in_background":"true"}`, `{}`, true},
		{`{"a":1,"run_in_background":false}`, `{"a":1}`, false},
		{`{"a":1}`, `{"a":1}`, false},
		{`oops`, `oops`, false},
	}
	for _, c := range cases {
		out, bg := splitBackground(c.in)
		if out != c.out || bg != c.bg {
			t.Errorf("splitBackground(%s) = %s, %v；期望 %s, %v", c.in, out, bg, c.out, c.bg)
		}
	}
}
