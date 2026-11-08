package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent/event"
	"github.com/shumengya/sprout/internal/agent/tool"
)

func TestProviderErrorRollsBackAndAgentStaysUsable(t *testing.T) {
	perr := &ProviderError{Status: 401, Message: "bad key"}
	p := &scripted{steps: []scriptStep{{err: perr}, say("好了")}}
	a := newAgent(t, p, nil, nil)
	var c collector

	err := a.Run(context.Background(), "q", &c)
	var got *ProviderError
	if !errors.As(err, &got) || got.Status != 401 {
		t.Fatalf("应返回 ProviderError，得到 %v", err)
	}
	want := []event.Kind{event.KindTurnStarted, event.KindError, event.KindTurnFinished}
	if !slices.Equal(c.kinds(), want) || c.finishedReason(t) != event.ReasonError {
		t.Fatalf("事件 %v，reason %q", c.kinds(), c.finishedReason(t))
	}
	if e := c.events[1].(event.Error); !strings.Contains(e.Message, "401") || e.Retryable {
		t.Errorf("Error 事件 = %+v", e)
	}
	if len(a.History()) != 0 {
		t.Fatalf("失败的首轮应撤销用户消息，历史 = %+v", a.History())
	}

	if err := a.Run(context.Background(), "重试", &collector{}); err != nil {
		t.Fatal(err)
	}
	if n := len(p.reqs[1].Messages); n != 1 {
		t.Fatalf("重试时历史里不应有重复提问，消息数 %d", n)
	}
}

func TestRetryableFlagPropagates(t *testing.T) {
	p := &scripted{steps: []scriptStep{{err: &ProviderError{Status: 503, Retryable: true}}}}
	var c collector
	_ = newAgent(t, p, nil, nil).Run(context.Background(), "q", &c)
	if e := c.events[1].(event.Error); !e.Retryable {
		t.Fatalf("Retryable 应透传: %+v", e)
	}
}

func TestMaxStepsStopsRunawayToolLoop(t *testing.T) {
	loop := callTools(call("c", "bash", `{}`))
	steps := make([]scriptStep, 20)
	for i := range steps {
		steps[i] = loop
	}
	p := &scripted{steps: steps}
	a := newAgent(t, p, registry(t, okTool("bash", tool.Result{})), nil)
	var c collector

	err := a.Run(context.Background(), "q", &c)
	if !errors.Is(err, ErrMaxSteps) {
		t.Fatalf("应返回 ErrMaxSteps，得到 %v", err)
	}
	if len(p.reqs) != DefaultMaxSteps {
		t.Fatalf("应恰好调用模型 %d 次，实际 %d", DefaultMaxSteps, len(p.reqs))
	}
	if c.finishedReason(t) != event.ReasonMaxSteps || !c.has(event.KindError) {
		t.Fatalf("应有 Error 事件且 reason=max_steps，得到 %v / %q", c.kinds(), c.finishedReason(t))
	}
	// 历史以 tool 结果结尾，仍是合法序列。
	h := a.History()
	if h[len(h)-1].Role != RoleTool {
		t.Fatalf("历史应以 tool 消息结尾: %+v", h[len(h)-1])
	}
}

func TestCancelDuringStreamRollsBack(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &scripted{steps: []scriptStep{{chunks: []string{"半截"}, hook: func(context.Context) { cancel() }}, say("ok")}}
	a := newAgent(t, p, nil, nil)
	var c collector

	err := a.Run(ctx, "q", &c)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("应返回 context.Canceled，得到 %v", err)
	}
	if c.has(event.KindError) || c.finishedReason(t) != event.ReasonCancelled {
		t.Fatalf("取消不是错误：%v / %q", c.kinds(), c.finishedReason(t))
	}
	if len(a.History()) != 0 {
		t.Fatalf("历史应回滚，得到 %+v", a.History())
	}
	if err := a.Run(context.Background(), "再来", &collector{}); err != nil {
		t.Fatal(err)
	}
}

func TestCancelDuringToolPadsRemainingCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	slow := fakeTool{name: "slow", run: func(ctx context.Context, _ json.RawMessage) (tool.Result, error) {
		cancel()
		return tool.Result{Command: "sleep 99", Stdout: "部分输出"}, ctx.Err()
	}}
	never := fakeTool{name: "never", run: func(context.Context, json.RawMessage) (tool.Result, error) {
		t.Error("取消后不应再执行后续工具")
		return tool.Result{}, nil
	}}
	p := &scripted{steps: []scriptStep{
		callTools(call("a", "slow", `{}`), call("b", "never", `{}`)),
		say("收到"),
	}}
	a := newAgent(t, p, registry(t, slow, never), nil)
	var c collector

	if err := a.Run(ctx, "q", &c); !errors.Is(err, context.Canceled) {
		t.Fatalf("得到 %v", err)
	}
	if c.finishedReason(t) != event.ReasonCancelled {
		t.Fatalf("reason = %q", c.finishedReason(t))
	}
	var fins []event.ToolFinished
	for _, e := range c.events {
		if f, ok := e.(event.ToolFinished); ok {
			fins = append(fins, f)
		}
	}
	if len(fins) != 2 || !fins[0].Cancelled || !fins[1].Cancelled || fins[0].Stdout != "部分输出" {
		t.Fatalf("两个调用都应标记为已取消，第一个保留部分输出: %+v", fins)
	}

	// 历史里每个 tool call 都必须有对应的 tool 消息，否则下一次请求会被服务端拒绝。
	h := a.History()
	if len(h) != 4 || h[2].ToolID != "a" || h[3].ToolID != "b" {
		t.Fatalf("历史 = %+v", h)
	}
	if !strings.Contains(h[2].Content, "部分输出") || !strings.Contains(h[3].Content, "未执行") {
		t.Fatalf("tool 消息内容不对: %q / %q", h[2].Content, h[3].Content)
	}
	if err := a.Run(context.Background(), "继续", &collector{}); err != nil {
		t.Fatal(err)
	}
}

func TestPanicIsRecoveredAndHistoryRestored(t *testing.T) {
	boom := fakeTool{name: "boom", run: func(context.Context, json.RawMessage) (tool.Result, error) { panic("炸了") }}
	p := &scripted{steps: []scriptStep{callTools(call("x", "boom", `{}`)), say("ok")}}
	a := newAgent(t, p, registry(t, boom), nil)
	var c collector

	err := a.Run(context.Background(), "q", &c)
	if err == nil || !strings.Contains(err.Error(), "炸了") {
		t.Fatalf("panic 应转成 error，得到 %v", err)
	}
	if !c.has(event.KindError) || c.finishedReason(t) != event.ReasonError {
		t.Fatalf("panic 后仍应以 TurnFinished 收尾：%v", c.kinds())
	}
	if len(a.History()) != 0 {
		t.Fatalf("整轮作废，历史应回到本轮开始前: %+v", a.History())
	}
}

func TestLengthStopWithoutToolCalls(t *testing.T) {
	p := &scripted{steps: []scriptStep{{
		chunks: []string{"被截"}, resp: Response{Message: Message{Content: "被截"}, StopReason: StopLength},
	}}}
	var c collector
	if err := newAgent(t, p, nil, nil).Run(context.Background(), "q", &c); err != nil {
		t.Fatalf("长度截断不是错误: %v", err)
	}
	if c.finishedReason(t) != event.ReasonLength {
		t.Fatalf("reason = %q", c.finishedReason(t))
	}
}

func TestEmptyToolArgumentsNormalized(t *testing.T) {
	var gotArgs string
	spy := fakeTool{name: "t", run: func(_ context.Context, a json.RawMessage) (tool.Result, error) {
		gotArgs = string(a)
		return tool.Result{}, nil
	}}
	p := &scripted{steps: []scriptStep{callTools(call("1", "t", "")), say("ok")}}
	a := newAgent(t, p, registry(t, spy), nil)
	if err := a.Run(context.Background(), "q", &collector{}); err != nil {
		t.Fatal(err)
	}
	if gotArgs != "{}" {
		t.Errorf("工具收到的参数 = %q，期望 {}", gotArgs)
	}
	if args := p.reqs[1].Messages[1].ToolCalls[0].Arguments; args != "{}" {
		t.Errorf("回传给模型的历史里参数 = %q，期望 {}（空串不是合法 JSON）", args)
	}
}

func TestProviderErrorMessagesAreChinese(t *testing.T) {
	for status, want := range map[int]string{
		401: "API Key", 403: "没有权限", 404: "不存在", 429: "额度", 500: "模型服务出错", 0: "无法连接",
	} {
		if msg := (&ProviderError{Status: status}).Error(); !strings.Contains(msg, want) {
			t.Errorf("status %d 的提示 %q 应含 %q", status, msg, want)
		}
	}
	long := &ProviderError{Status: 400, Code: "context_length_exceeded", Message: "too long"}
	if !strings.Contains(long.Error(), "上下文超出") || !strings.Contains(long.Error(), "too long") {
		t.Errorf("超长上下文提示不对: %s", long)
	}
	cause := fmt.Errorf("dial tcp: refused")
	pe := &ProviderError{Err: cause, Retryable: true}
	if !errors.Is(pe, cause) || !strings.Contains(pe.Error(), "refused") {
		t.Errorf("应能 Unwrap 到底层错误: %v", pe)
	}
}
