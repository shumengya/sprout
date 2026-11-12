package agent

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent/event"
	"github.com/shumengya/sprout/internal/agent/tool"
)

func TestNewValidates(t *testing.T) {
	if _, err := New(Config{Model: "m"}); err == nil {
		t.Error("缺少 Provider 应报错")
	}
	if _, err := New(Config{Provider: &scripted{}}); err == nil {
		t.Error("缺少 Model 应报错")
	}
	a, err := New(Config{Provider: &scripted{}, Model: "m"})
	// 默认不限轮数：0 表示没有上限。
	if err != nil || a.cfg.MaxSteps != 0 {
		t.Errorf("MaxSteps 缺省应为 0（不限），得到 %v, %v", a.cfg.MaxSteps, err)
	}
}

func TestPlainAnswer(t *testing.T) {
	p := &scripted{steps: []scriptStep{{
		chunks: []string{"你", "好"},
		resp:   Response{Message: Message{Content: []Block{TextBlock("你好")}}, Usage: Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}, StopReason: StopStop},
	}}}
	a := newAgent(t, p, nil, nil)
	var c collector

	if err := a.Run(context.Background(), Input{Text: "hi"}, &c); err != nil {
		t.Fatal(err)
	}

	want := []event.Kind{
		event.KindTurnStarted, event.KindMessageDelta, event.KindMessageDelta,
		event.KindMessageCompleted, event.KindUsage, event.KindTurnFinished,
	}
	if !slices.Equal(c.kinds(), want) {
		t.Fatalf("事件序列 = %v，期望 %v", c.kinds(), want)
	}
	if c.finishedReason(t) != event.ReasonStop {
		t.Errorf("reason = %q", c.finishedReason(t))
	}
	if u := c.events[4].(event.Usage); u.TotalTokens != 5 {
		t.Errorf("Usage = %+v", u)
	}
	h := a.History()
	if len(h) != 2 || h[0].Role != RoleUser || h[1].Role != RoleAssistant || h[1].Text() != "你好" {
		t.Fatalf("历史 = %+v", h)
	}
}

func TestRequestCarriesSystemModelToolsAndLimit(t *testing.T) {
	p := &scripted{steps: []scriptStep{say("ok")}}
	a := newAgent(t, p, registry(t, okTool("bash", tool.Result{})), nil)
	if err := a.Run(context.Background(), Input{Text: "q"}, &collector{}); err != nil {
		t.Fatal(err)
	}
	r := p.reqs[0]
	if r.Model != "test-model" || r.System != "SYS" || r.MaxOutputTokens != 123 {
		t.Errorf("请求参数不对: %+v", r)
	}
	if len(r.Tools) != 1 || r.Tools[0].Name != "bash" || r.Tools[0].Description != "desc bash" {
		t.Errorf("工具声明不对: %+v", r.Tools)
	}
	if len(r.Messages) != 1 || r.Messages[0].Role != RoleUser {
		t.Errorf("消息不对: %+v", r.Messages)
	}
}

func TestToolLoopFeedsResultBackToModel(t *testing.T) {
	bash := okTool("bash", tool.Result{Command: "ls", Stdout: "main.go\n", ExitCode: 0})
	p := &scripted{steps: []scriptStep{
		callTools(call("c1", "bash", `{"command":"ls"}`)),
		say("入口是 main.go"),
	}}
	a := newAgent(t, p, registry(t, bash), nil)
	var c collector

	if err := a.Run(context.Background(), Input{Text: "找入口"}, &c); err != nil {
		t.Fatal(err)
	}

	want := []event.Kind{
		event.KindTurnStarted,
		event.KindMessageCompleted, event.KindToolRequested, event.KindToolFinished,
		event.KindMessageDelta, event.KindMessageCompleted, event.KindTurnFinished,
	}
	if !slices.Equal(c.kinds(), want) {
		t.Fatalf("事件序列 = %v，期望 %v", c.kinds(), want)
	}
	if len(p.reqs) != 2 {
		t.Fatalf("应调用模型 2 次，实际 %d", len(p.reqs))
	}
	second := p.reqs[1].Messages
	if len(second) != 3 || second[1].Role != RoleAssistant || len(second[1].ToolCalls) != 1 {
		t.Fatalf("第二次请求应带上 assistant 的 tool call: %+v", second)
	}
	tm := second[2]
	if tm.Role != RoleTool || tm.ToolID != "c1" || !strings.Contains(tm.Text(), "main.go") ||
		!strings.Contains(tm.Text(), "exit_code: 0") {
		t.Fatalf("tool 消息不对: %+v", tm)
	}
	fin := c.events[3].(event.ToolFinished)
	if fin.Command != "ls" || fin.Stdout != "main.go\n" || fin.ID != "c1" {
		t.Errorf("ToolFinished = %+v", fin)
	}
}

func TestHistoryAccumulatesAcrossRuns(t *testing.T) {
	p := &scripted{steps: []scriptStep{say("一"), say("二")}}
	a := newAgent(t, p, nil, nil)
	for _, q := range []string{"q1", "q2"} {
		if err := a.Run(context.Background(), Input{Text: q}, &collector{}); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(p.reqs[1].Messages); got != 3 {
		t.Fatalf("第二轮请求应带上第一轮历史（3 条），实际 %d", got)
	}
	if got := len(a.History()); got != 4 {
		t.Fatalf("历史应有 4 条，实际 %d", got)
	}
}

func TestReasoningFlowsThroughAgentAndIsReplayed(t *testing.T) {
	// 思维链要进入历史并随下一次请求回传，同时作为事件发给 surface 和会话记录。
	think := scriptStep{
		chunks: nil,
		resp: Response{Message: Message{Reasoning: "先看目录", ToolCalls: []ToolCall{call("c1", "bash", `{}`)}},
			StopReason: StopToolCalls},
	}
	p := &scripted{steps: []scriptStep{think, say("好了")}}
	a := newAgent(t, p, registry(t, okTool("bash", tool.Result{Command: "ls"})), nil)
	var c collector
	if err := a.Run(context.Background(), Input{Text: "q"}, &c); err != nil {
		t.Fatal(err)
	}
	second := p.reqs[1].Messages
	if second[1].Reasoning != "先看目录" {
		t.Fatalf("下一次请求的历史里应带上 Reasoning: %+v", second[1])
	}
	mc := c.events[1].(event.MessageCompleted)
	if mc.Reasoning != "先看目录" {
		t.Errorf("MessageCompleted.Reasoning = %q", mc.Reasoning)
	}
}

func TestReasoningDeltasBecomeEvents(t *testing.T) {
	p := &scripted{steps: []scriptStep{{
		chunks:          []string{"答"},
		resp:            Response{Message: Message{Content: []Block{TextBlock("答")}}, StopReason: StopStop},
		reasoningChunks: []string{"想", "想"},
	}}}
	var c collector
	if err := newAgent(t, p, nil, nil).Run(context.Background(), Input{Text: "q"}, &c); err != nil {
		t.Fatal(err)
	}
	want := []event.Kind{event.KindTurnStarted, event.KindReasoningDelta, event.KindReasoningDelta,
		event.KindMessageDelta, event.KindMessageCompleted, event.KindTurnFinished}
	if !slices.Equal(c.kinds(), want) {
		t.Fatalf("事件序列 = %v，期望 %v", c.kinds(), want)
	}
}

func TestHistoryIsACopy(t *testing.T) {
	a := newAgent(t, &scripted{steps: []scriptStep{say("x")}}, nil, nil)
	_ = a.Run(context.Background(), Input{Text: "q"}, &collector{})
	h := a.History()
	h[0].Content = []Block{TextBlock("被篡改")}
	if a.History()[0].Text() != "q" {
		t.Fatal("History 应返回副本")
	}
}

func TestRecorderSeesSameEventsAsSink(t *testing.T) {
	var sink, rec collector
	a := newAgent(t, &scripted{steps: []scriptStep{say("x")}}, nil, &rec)
	if err := a.Run(context.Background(), Input{Text: "q"}, &sink); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sink.kinds(), rec.kinds()) || len(rec.events) == 0 {
		t.Fatalf("recorder %v 与 sink %v 应一致", rec.kinds(), sink.kinds())
	}
}

func TestNilSinkIsAllowed(t *testing.T) {
	a := newAgent(t, &scripted{steps: []scriptStep{say("x")}}, nil, nil)
	if err := a.Run(context.Background(), Input{Text: "q"}, nil); err != nil {
		t.Fatal(err)
	}
}
