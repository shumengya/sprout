package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shumengya/sprout/internal/agent/event"
	"github.com/shumengya/sprout/internal/agent/tool"
)

// 工具经 tool.Call.Emit 转发的事件要落在自己那次调用的 tool.requested 与 tool.finished 之间，
// 并且和其他事件一样由主 goroutine 发出（collector 不加锁，-race 下也能发现问题）。
func TestToolProgressIsForwardedBetweenRequestedAndFinished(t *testing.T) {
	emitter := fakeTool{name: "emit", run: func(ctx context.Context, _ json.RawMessage) (tool.Result, error) {
		c, ok := tool.CallFrom(ctx)
		if !ok || c.Emit == nil {
			t.Error("内核执行工具时 ctx 里应带着 tool.Call")
			return tool.Result{}, nil
		}
		for range 3 {
			c.Emit(event.Subagent{CallID: c.ID, Agent: "x", Event: event.Usage{OutputTokens: 1}})
		}
		return tool.Result{Command: c.ID}, nil
	}}
	p := &scripted{steps: []scriptStep{
		callTools(call("a", "emit", "{}"), call("b", "emit", "{}")),
		say("好了"),
	}}
	var rec collector
	ag := newAgent(t, p, registry(t, emitter), &rec)
	var c collector
	if err := ag.Run(context.Background(), Input{Text: "q"}, &c); err != nil {
		t.Fatal(err)
	}

	open := map[string]bool{}
	seen := map[string]int{}
	for _, e := range c.events {
		switch e := e.(type) {
		case event.ToolRequested:
			open[e.Call.ID] = true
		case event.ToolFinished:
			if seen[e.ID] != 3 {
				t.Errorf("调用 %s 结束前应收到 3 个转发事件，得到 %d", e.ID, seen[e.ID])
			}
			delete(open, e.ID)
		case event.Subagent:
			if !open[e.CallID] {
				t.Errorf("调用 %s 的转发事件落在 requested/finished 之外", e.CallID)
			}
			seen[e.CallID]++
		}
	}
	if len(rec.events) != len(c.events) {
		t.Errorf("Recorder 应收到同样的事件：%d vs %d", len(rec.events), len(c.events))
	}
	// 转发事件不进历史：历史里仍是 user、assistant、两条 tool、assistant。
	if h := ag.History(); len(h) != 5 {
		t.Errorf("历史长度 = %d", len(h))
	}
}
