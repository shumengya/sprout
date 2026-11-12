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

	err := a.Run(context.Background(), Input{Text: "q"}, &c)
	var got *ProviderError
	if !errors.As(err, &got) || got.Status != 401 {
		t.Fatalf("应返回 ProviderError，得到 %v", err)
	}
	want := []event.Kind{event.KindTurnStarted, event.KindInterrupted, event.KindError, event.KindTurnFinished}
	if !slices.Equal(c.kinds(), want) || c.finishedReason(t) != event.ReasonError {
		t.Fatalf("事件 %v，reason %q", c.kinds(), c.finishedReason(t))
	}
	if e := c.events[2].(event.Error); !strings.Contains(e.Message, "401") || e.Retryable {
		t.Errorf("Error 事件 = %+v", e)
	}
	if e := c.events[1].(event.Interrupted); !strings.Contains(e.Content, "中断") {
		t.Errorf("Interrupted 事件应带中断说明: %+v", e)
	}
	// 失败的那轮保留在历史里：用户消息之后补一条带原因的 assistant 消息。
	h := a.History()
	if len(h) != 2 || h[0].Role != RoleUser || h[1].Role != RoleAssistant {
		t.Fatalf("历史 = %+v", h)
	}
	if !strings.Contains(h[1].Text(), "中断") {
		t.Errorf("中断说明 = %q", h[1].Text())
	}

	if err := a.Run(context.Background(), Input{Text: "重试"}, &collector{}); err != nil {
		t.Fatal(err)
	}
	// 两条 user 消息之间有 assistant 说明，不会出现连续重复提问。
	msgs := p.reqs[1].Messages
	if len(msgs) != 3 || msgs[0].Role != RoleUser || msgs[1].Role != RoleAssistant || msgs[2].Role != RoleUser {
		t.Fatalf("重试时的历史 = %+v", msgs)
	}
}

func TestRetryableFlagPropagates(t *testing.T) {
	p := &scripted{steps: []scriptStep{{err: &ProviderError{Status: 503, Retryable: true}}}}
	var c collector
	_ = newAgent(t, p, nil, nil).Run(context.Background(), Input{Text: "q"}, &c)
	var found bool
	for _, e := range c.events {
		if err, ok := e.(event.Error); ok {
			found = true
			if !err.Retryable {
				t.Fatalf("Retryable 应透传: %+v", err)
			}
		}
	}
	if !found {
		t.Fatal("应有 Error 事件")
	}
}

func TestMaxStepsStopsRunawayToolLoop(t *testing.T) {
	// 默认不限轮数，这里显式设一个上限，确认配置生效且停得下来。
	const limit = 3
	loop := callTools(call("c", "bash", `{}`))
	steps := make([]scriptStep, 20)
	for i := range steps {
		steps[i] = loop
	}
	p := &scripted{steps: steps}
	a := newAgentCfg(t, Config{Provider: p, Tools: registry(t, okTool("bash", tool.Result{})), Retry: &RetryPolicy{}, MaxSteps: limit})
	var c collector

	err := a.Run(context.Background(), Input{Text: "q"}, &c)
	if !errors.Is(err, ErrMaxSteps) {
		t.Fatalf("应返回 ErrMaxSteps，得到 %v", err)
	}
	if len(p.reqs) != limit {
		t.Fatalf("应恰好调用模型 %d 次，实际 %d", limit, len(p.reqs))
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

func TestCancelDuringStreamKeepsPartialOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &scripted{steps: []scriptStep{{chunks: []string{"半截"}, hook: func(context.Context) { cancel() }}, say("ok")}}
	a := newAgent(t, p, nil, nil)
	var c collector

	err := a.Run(ctx, Input{Text: "q"}, &c)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("应返回 context.Canceled，得到 %v", err)
	}
	if c.has(event.KindError) || c.finishedReason(t) != event.ReasonCancelled {
		t.Fatalf("取消不是错误：%v / %q", c.kinds(), c.finishedReason(t))
	}
	// 已经输出的半截内容连同中断原因留在历史里，用户说“继续”时模型知道自己说到哪了。
	h := a.History()
	if len(h) != 2 || h[1].Role != RoleAssistant {
		t.Fatalf("历史 = %+v", h)
	}
	if text := h[1].Text(); !strings.HasPrefix(text, "半截") || !strings.Contains(text, "用户取消") {
		t.Fatalf("中断消息 = %q", text)
	}
	// 中断事件带同一条文本，界面按它展示；Partial 表示正文已经流式输出过。
	var ev event.Interrupted
	found := false
	for _, e := range c.events {
		if i, ok := e.(event.Interrupted); ok {
			ev, found = i, true
		}
	}
	if !found || !ev.Partial || ev.Content != h[1].Text() || ev.Reason != event.ReasonCancelled {
		t.Fatalf("事件 = %+v", c.events)
	}
	if err := a.Run(context.Background(), Input{Text: "再来"}, &collector{}); err != nil {
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
	// 取消阻断后续调用是串行语义（并发时第二个也会同时开始），所以显式固定为 1。
	a := newAgentCfg(t, Config{Provider: p, Tools: registry(t, slow, never), Retry: &RetryPolicy{}, MaxParallelTools: 1})
	var c collector

	if err := a.Run(ctx, Input{Text: "q"}, &c); !errors.Is(err, context.Canceled) {
		t.Fatalf("得到 %v", err)
	}
	if c.finishedReason(t) != event.ReasonCancelled {
		t.Fatalf("reason = %q", c.finishedReason(t))
	}
	// ToolFinished 按完成顺序发出（被跳过的 b 先于还在收尾的 a），所以按 ID 取，不依赖事件顺序。
	fins := map[string]event.ToolFinished{}
	for _, e := range c.events {
		if f, ok := e.(event.ToolFinished); ok {
			fins[f.ID] = f
		}
	}
	if len(fins) != 2 || !fins["a"].Cancelled || !fins["b"].Cancelled || fins["a"].Stdout != "部分输出" {
		t.Fatalf("两个调用都应标记为已取消，第一个保留部分输出: %+v", fins)
	}

	// 历史里每个 tool call 都必须有对应的 tool 消息，否则下一次请求会被服务端拒绝；
	// 末尾再补一条中断说明，模型知道这轮为什么停。
	h := a.History()
	if len(h) != 5 || h[2].ToolID != "a" || h[3].ToolID != "b" || h[4].Role != RoleAssistant {
		t.Fatalf("历史 = %+v", h)
	}
	if !strings.Contains(h[2].Text(), "部分输出") || !strings.Contains(h[3].Text(), "未执行") || !strings.Contains(h[4].Text(), "用户取消") {
		t.Fatalf("消息内容不对: %q / %q / %q", h[2].Text(), h[3].Text(), h[4].Text())
	}
	if err := a.Run(context.Background(), Input{Text: "继续"}, &collector{}); err != nil {
		t.Fatal(err)
	}
}

func TestToolPanicBecomesToolFailureAndTurnContinues(t *testing.T) {
	// 工具实现的 bug 只算这一次工具调用失败，告诉模型，不作废整轮，也不影响同批的其他调用。
	boom := fakeTool{name: "boom", run: func(context.Context, json.RawMessage) (tool.Result, error) { panic("炸了") }}
	fine := okTool("fine", tool.Result{Command: "ok", Stdout: "好的"})
	p := &scripted{steps: []scriptStep{
		callTools(call("x", "boom", `{}`), call("y", "fine", `{}`)),
		say("收到"),
	}}
	a := newAgent(t, p, registry(t, boom, fine), nil)
	var c collector

	if err := a.Run(context.Background(), Input{Text: "q"}, &c); err != nil {
		t.Fatalf("工具 panic 不应让整轮失败: %v", err)
	}
	h := a.History()
	if len(h) != 5 || !strings.Contains(h[2].Text(), "内部错误: 炸了") || !strings.Contains(h[3].Text(), "好的") {
		t.Fatalf("历史 = %+v", h)
	}
	if c.finishedReason(t) != event.ReasonStop {
		t.Errorf("reason = %q", c.finishedReason(t))
	}
}

func TestProviderPanicIsRecoveredAndHistoryRestored(t *testing.T) {
	// Run 顶层的 recover：provider 的 panic 作废整轮，历史还原，仍以 TurnFinished 收尾。
	p := &scripted{steps: []scriptStep{
		{hook: func(context.Context) { panic("炸了") }},
		say("ok"),
	}}
	a := newAgent(t, p, nil, nil)
	var c collector

	err := a.Run(context.Background(), Input{Text: "q"}, &c)
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
		chunks: []string{"被截"}, resp: Response{Message: Message{Content: []Block{TextBlock("被截")}}, StopReason: StopLength},
	}}}
	var c collector
	if err := newAgent(t, p, nil, nil).Run(context.Background(), Input{Text: "q"}, &c); err != nil {
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
	if err := a.Run(context.Background(), Input{Text: "q"}, &collector{}); err != nil {
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
		401: "API Key", 403: "没有权限", 404: "不存在", 429: "额度", 500: "模型服务出错", 0: "通信失败",
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
