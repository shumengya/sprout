package session

import (
	"context"
	"reflect"
	"testing"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/event"
	"github.com/shumengya/sprout/internal/agent/tool"
)

// 一轮中途接进来的排队输入（含图片）落盘为 user_queued，重放出的历史与运行时逐条一致，
// 而且仍算同一轮。
func TestReplayReproducesQueuedInputExactly(t *testing.T) {
	prov := funcProvider(func(req agent.Request) agent.Response {
		if last := req.Messages[len(req.Messages)-1]; last.Role == agent.RoleUser && last.Text() == "开始" {
			return calls("", agent.ToolCall{ID: "c1", Name: "a", Arguments: "{}"})
		}
		return answer("收到补充")
	})
	okA := funcTool{name: "a", run: func(context.Context) (tool.Result, error) { return tool.Result{Stdout: "ok"}, nil }}

	rec := NewRecorder(t.TempDir(), Info{Provider: "p", Model: "m", Cwd: `D:\projq`}, nil)
	ag, err := agent.New(agent.Config{Provider: prov, Model: "m", System: "SYS", Recorder: rec, Vision: true,
		Tools: registryOf(t, okA), Retry: &agent.RetryPolicy{}})
	if err != nil {
		t.Fatal(err)
	}
	pending := []agent.Input{{Text: "补充", Images: []agent.Image{{Name: "b.png", MediaType: "image/png", Data: testPNG(t)}}}}
	queued := func() []agent.Input {
		out := pending
		pending = nil
		return out
	}
	if err := ag.Run(context.Background(), agent.Input{Text: "开始", Queued: queued}, event.SinkFunc(func(event.Event) {})); err != nil {
		t.Fatal(err)
	}
	rec.Close()

	st, err := Load(rec.Path())
	if err != nil {
		t.Fatal(err)
	}
	if roles(st.History) != "user,assistant,tool,user,assistant" {
		t.Fatalf("历史 = %s", roles(st.History))
	}
	if !reflect.DeepEqual(st.History, ag.History()) {
		t.Fatalf("历史不一致：\n重放:   %+v\n运行时: %+v", st.History, ag.History())
	}
	if st.Turns != 1 {
		t.Errorf("排队输入不算新的一轮，Turns = %d", st.Turns)
	}
	var got *event.UserQueued
	for _, e := range st.Events {
		if uq, ok := e.(event.UserQueued); ok {
			got = &uq
		}
	}
	if got == nil || got.Prompt != "补充" || len(got.Images) != 1 || got.Images[0].Name != "b.png" {
		t.Fatalf("重放事件里应有带图片的 UserQueued: %+v", got)
	}
}

// 并行工具的结果按完成顺序落盘；user_queued 前要先把结果按调用顺序补齐，否则消息顺序与运行时不同。
func TestQueuedRecordComesAfterAllToolResults(t *testing.T) {
	st := load(t, u1, call, r2, r1, `{"type":"user_queued","content":"补充"}`, a1, t1)
	if roles(st.History) != "user,assistant,tool,tool,user,assistant" || st.Turns != 1 {
		t.Fatalf("历史 %s，轮数 %d", roles(st.History), st.Turns)
	}
	if st.History[2].ToolID != "c1" || st.History[4].Text() != "补充" {
		t.Errorf("顺序不对: %+v", st.History)
	}
}
