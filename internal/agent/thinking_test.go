package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shumengya/sprout/internal/agent/event"
	"github.com/shumengya/sprout/internal/agent/tool"
)

// 初始思考参数随请求下发；SetThinking 在生成中调用，从下一次模型请求开始生效。
func TestThinkingFlowsIntoRequestsAndCanChangeMidTurn(t *testing.T) {
	var a *Agent
	p := &scripted{steps: []scriptStep{
		// 第一次请求进行中把档位调高：本次请求不受影响，工具执行后的下一次请求生效。
		{hook: func(context.Context) { a.SetThinking(Thinking{Level: "max", Effort: "max"}) },
			resp: Response{Message: Message{ToolCalls: []ToolCall{call("c1", "t", "{}")}}, StopReason: StopToolCalls}},
		say("好"),
	}}
	a = newAgentCfg(t, Config{Provider: p, Tools: registry(t, okTool("t", tool.Result{Command: "t"})), Retry: &RetryPolicy{},
		Thinking: Thinking{Level: "low", Effort: "low"}})

	if err := a.Run(context.Background(), Input{Text: "q"}, &collector{}); err != nil {
		t.Fatal(err)
	}
	if got := p.reqs[0].Thinking.Effort; got != "low" {
		t.Errorf("第一次请求 effort = %q", got)
	}
	if got := p.reqs[1].Thinking.Effort; got != "max" {
		t.Errorf("调整后的请求 effort = %q", got)
	}
	if a.Thinking().Level != "max" {
		t.Errorf("Thinking() = %+v", a.Thinking())
	}
}

// 上下文摘要是内部请求，不带思考参数：既省 token，也避免预算模式下的输出上限冲突。
func TestSummaryRequestHasNoThinking(t *testing.T) {
	p := &scripted{steps: []scriptStep{say("摘要"), say("最终回答")}}
	a := newAgentCfg(t, Config{Provider: p, ContextWindow: 800, Retry: &RetryPolicy{},
		Thinking: Thinking{Effort: "high", Extra: map[string]json.RawMessage{"x": json.RawMessage(`1`)}}})
	a.Restore(textTurns(8, 100), "")
	if err := a.Run(context.Background(), Input{Text: "新问题"}, &collector{}); err != nil {
		t.Fatal(err)
	}
	if len(p.reqs) != 2 || !isSummaryReq(p.reqs[0]) {
		t.Fatalf("应先发摘要请求: %d 次", len(p.reqs))
	}
	if th := p.reqs[0].Thinking; th.Active() || th.Extra != nil {
		t.Errorf("摘要请求带了思考参数: %+v", th)
	}
	if p.reqs[1].Thinking.Effort != "high" {
		t.Errorf("正式请求应带思考参数: %+v", p.reqs[1].Thinking)
	}
}

// provider 给出的原始内容进历史、进事件，换模型时随思维链一起清掉。
func TestNativeKeptInHistoryAndEvent(t *testing.T) {
	native := &Native{Protocol: "anthropic-messages", Model: "test-model", Data: json.RawMessage(`[{"type":"thinking"}]`)}
	p := &scripted{steps: []scriptStep{{resp: Response{
		Message: Message{Content: []Block{TextBlock("答")}, Reasoning: "想", Native: native}, StopReason: StopStop,
	}}}}
	a := newAgent(t, p, nil, nil)
	var c collector
	if err := a.Run(context.Background(), Input{Text: "q"}, &c); err != nil {
		t.Fatal(err)
	}
	h := a.History()
	if h[1].Native != native {
		t.Fatalf("历史里的 Native = %+v", h[1].Native)
	}
	var done *event.MessageCompleted
	for _, e := range c.events {
		if mc, ok := e.(event.MessageCompleted); ok {
			done = &mc
		}
	}
	if done == nil || done.Native != native {
		t.Fatalf("MessageCompleted.Native = %+v", done)
	}
	if s := h[1].StripReasoning(); s.Reasoning != "" || s.Native != nil || s.Text() != "答" {
		t.Errorf("StripReasoning = %+v", s)
	}
	if !native.Usable("anthropic-messages", "test-model") || native.Usable("anthropic-messages", "other") ||
		native.Usable("openai-chat", "test-model") || (*Native)(nil).Usable("anthropic-messages", "test-model") {
		t.Error("Usable 判断不对")
	}
}

// 裁剪工具输出改了历史中段：之后的思考块都对不上前缀，必须全部丢掉，否则服务端会拒绝。
func TestPruneDropsNative(t *testing.T) {
	native := &Native{Protocol: "anthropic-messages", Model: "m", Data: json.RawMessage(`[]`)}
	long := make([]rune, pruneMinChars+10)
	for i := range long {
		long[i] = 'x'
	}
	h := []Message{
		{Role: RoleTool, Content: []Block{TextBlock(string(long))}},
		{Role: RoleAssistant, Content: []Block{TextBlock("a")}, Reasoning: "r", Native: native},
	}
	if PruneToolOutputs(h, 0, 0) != 1 || h[1].Native != nil || h[1].Reasoning != "r" {
		t.Fatalf("裁剪后应清掉 Native、保留思维链文本: %+v", h[1])
	}
	h[1].Native = native
	if PruneToolOutputs(h, 0, 0) != 0 || h[1].Native == nil {
		t.Error("没有裁剪时不应动 Native")
	}
}
