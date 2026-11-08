package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/shumengya/sprout/internal/agent/event"
	"github.com/shumengya/sprout/internal/agent/tool"
)

// scriptStep 是脚本化 provider 的一次调用：先推送 chunks，再按 err / resp 返回。
// hook 在推送前执行，用来模拟取消或 panic。
type scriptStep struct {
	chunks []string
	resp   Response
	err    error
	hook   func(ctx context.Context)
}

// scripted 按顺序回放脚本，并记录每次收到的请求。只用于测试。
type scripted struct {
	steps []scriptStep
	reqs  []Request
}

func (s *scripted) Stream(ctx context.Context, req Request, emit func(Chunk) error) (Response, error) {
	s.reqs = append(s.reqs, req)
	i := len(s.reqs) - 1
	if i >= len(s.steps) {
		return Response{}, errors.New("脚本已用完")
	}
	st := s.steps[i]
	if st.hook != nil {
		st.hook(ctx)
	}
	for _, c := range st.chunks {
		if err := emit(Chunk{TextDelta: c}); err != nil {
			return Response{}, err
		}
	}
	if st.err != nil {
		return Response{}, st.err
	}
	return st.resp, nil
}

func say(text string) scriptStep {
	return scriptStep{chunks: []string{text}, resp: Response{Message: Message{Content: text}, StopReason: StopStop}}
}

func callTools(calls ...ToolCall) scriptStep {
	return scriptStep{resp: Response{Message: Message{ToolCalls: calls}, StopReason: StopToolCalls}}
}

func call(id, name, args string) ToolCall { return ToolCall{ID: id, Name: name, Arguments: args} }

// collector 收集事件。
type collector struct{ events []event.Event }

func (c *collector) Emit(e event.Event) { c.events = append(c.events, e) }

func (c *collector) kinds() []event.Kind {
	out := make([]event.Kind, len(c.events))
	for i, e := range c.events {
		out[i] = e.Kind()
	}
	return out
}

func (c *collector) last() event.Event { return c.events[len(c.events)-1] }

func (c *collector) finishedReason(t *testing.T) string {
	t.Helper()
	tf, ok := c.last().(event.TurnFinished)
	if !ok {
		t.Fatalf("最后一个事件应是 TurnFinished，得到 %T", c.last())
	}
	return tf.Reason
}

func (c *collector) has(k event.Kind) bool {
	for _, e := range c.events {
		if e.Kind() == k {
			return true
		}
	}
	return false
}

// fakeTool 是可编程的工具替身。只用于测试。
type fakeTool struct {
	name string
	run  func(ctx context.Context, args json.RawMessage) (tool.Result, error)
}

func (f fakeTool) Name() string                { return f.name }
func (f fakeTool) Description() string         { return "desc " + f.name }
func (f fakeTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (f fakeTool) Run(ctx context.Context, a json.RawMessage) (tool.Result, error) {
	return f.run(ctx, a)
}

func okTool(name string, res tool.Result) fakeTool {
	return fakeTool{name: name, run: func(context.Context, json.RawMessage) (tool.Result, error) { return res, nil }}
}

func registry(t *testing.T, tools ...tool.Tool) *tool.Registry {
	t.Helper()
	r := tool.NewRegistry()
	for _, tl := range tools {
		if err := r.Register(tl); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func newAgent(t *testing.T, p Provider, r *tool.Registry, rec event.Sink) *Agent {
	t.Helper()
	a, err := New(Config{Provider: p, Model: "test-model", System: "SYS", Tools: r, Recorder: rec, MaxOutputTokens: 123})
	if err != nil {
		t.Fatal(err)
	}
	return a
}
