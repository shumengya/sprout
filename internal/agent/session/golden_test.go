package session

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/event"
	"github.com/shumengya/sprout/internal/agent/tool"
)

// signalSink 把事件转给 Recorder，写完指定工具的 ToolFinished 之后关闭 done。
type signalSink struct {
	rec  *Recorder
	id   string
	done chan struct{}
	once sync.Once
}

func (s *signalSink) Emit(e event.Event) {
	s.rec.Emit(e)
	if f, ok := e.(event.ToolFinished); ok && f.ID == s.id {
		s.once.Do(func() { close(s.done) })
	}
}

type kinds struct{ got []event.Kind }

func (k *kinds) Emit(e event.Event) { k.got = append(k.got, e.Kind()) }

func (k *kinds) count(kind event.Kind) int {
	n := 0
	for _, g := range k.got {
		if g == kind {
			n++
		}
	}
	return n
}

// 黄金对照：用真实 agent 跑一段对话并落盘，再重放，恢复出的历史和摘要必须与运行时逐条一致。
// 对话里同时有：并行工具（完成顺序与调用顺序相反）、思维链、很大的工具输出（触发裁剪）、
// 多轮长回答（触发摘要压缩）。任何一处重放规则和运行时不一致，这里都会露出来。
func TestReplayReproducesLiveHistoryExactly(t *testing.T) {
	// a 一直等到 b 的 ToolFinished 已经写进会话记录才返回，所以记录里 b 一定先于 a。
	// 不靠 sleep：用包装过的 Recorder 在写完 b 的记录后发信号，顺序是确定的。
	bRecorded := make(chan struct{})
	slowA := funcTool{name: "a", run: func(ctx context.Context) (tool.Result, error) {
		select {
		case <-bRecorded:
		case <-ctx.Done():
		}
		return tool.Result{Command: "a", Stdout: "A 的输出"}, nil
	}}
	fastB := funcTool{name: "b", run: func(context.Context) (tool.Result, error) {
		return tool.Result{Command: "b", Stdout: "B 的输出"}, nil
	}}
	huge := funcTool{name: "huge", run: func(context.Context) (tool.Result, error) {
		// 单独不超预算，但后面的短轮次会把它推到“最近 6 条”之外，那时它就成了裁剪的对象。
		return tool.Result{Command: "huge", Stdout: strings.Repeat("大", 900)}, nil
	}}

	prov := funcProvider(func(req agent.Request) agent.Response {
		if isSummaryRequest(req) {
			return answer("这是压缩后的摘要")
		}
		last := req.Messages[len(req.Messages)-1]
		switch {
		case last.Role == agent.RoleUser && last.Content == "并行":
			return calls("先同时跑两个", agent.ToolCall{ID: "ca", Name: "a", Arguments: "{}"}, agent.ToolCall{ID: "cb", Name: "b", Arguments: "{}"})
		case last.Role == agent.RoleUser && last.Content == "大输出":
			return calls("", agent.ToolCall{ID: "ch", Name: "huge", Arguments: ""})
		case last.Role == agent.RoleTool:
			return answer("工具都跑完了：" + strings.Repeat("字", 120))
		}
		return agent.Response{Message: agent.Message{Reasoning: "想一想", Content: strings.Repeat("长回答", 20)}, StopReason: agent.StopStop}
	})

	dir := t.TempDir()
	rec := NewRecorder(dir, Info{Provider: "p", Model: "m", Cwd: `D:\proj`, Tools: []string{"a", "b", "huge"}}, nil)
	ag, err := agent.New(agent.Config{
		Provider: prov, Model: "m", System: "SYS", Recorder: &signalSink{rec: rec, id: "cb", done: bRecorded}, ContextWindow: 2500,
		Tools: registryOf(t, slowA, fastB, huge), Retry: &agent.RetryPolicy{},
	})
	if err != nil {
		t.Fatal(err)
	}

	var ev kinds
	questions := []string{"你好", "并行", "大输出"}
	for i := 1; i <= 24; i++ { // 先累积到越过预算触发裁剪，再继续累积触发摘要
		questions = append(questions, fmt.Sprintf("继续聊 %d", i))
	}
	for _, q := range questions {
		if err := ag.Run(context.Background(), q, &ev); err != nil {
			t.Fatalf("%q: %v", q, err)
		}
	}
	rec.Close()
	if ev.count(event.KindCompacted) == 0 {
		t.Fatal("测试前提不成立：这段对话应当触发过上下文压缩")
	}
	assertGoldenPremises(t, rec.Path())

	st, err := Load(rec.Path())
	if err != nil {
		t.Fatal(err)
	}
	live := ag.History()
	if len(st.History) != len(live) {
		t.Fatalf("历史条数不同：重放 %d，运行时 %d", len(st.History), len(live))
	}
	for i := range live {
		if !reflect.DeepEqual(st.History[i], live[i]) {
			t.Fatalf("第 %d 条不一致：\n重放:   %+v\n运行时: %+v", i, st.History[i], live[i])
		}
	}
	if st.Summary != ag.Summary() || st.Summary == "" {
		t.Errorf("摘要不一致：重放 %q，运行时 %q", st.Summary, ag.Summary())
	}
	if st.Turns != len(questions) || st.Skipped != 0 || st.ID == "" || st.Info.Model != "m" {
		t.Errorf("元信息不对: %+v", st)
	}
}

// assertGoldenPremises 确认黄金对照确实覆盖了它声称覆盖的场景。否则对照一致可能只是因为场景太简单。
func assertGoldenPremises(t *testing.T, path string) {
	t.Helper()
	modes := map[string]int{}
	var toolOrder []string
	for _, r := range readRecords(t, path) {
		switch r["type"] {
		case "compact":
			modes[r["mode"].(string)]++
		case "tool":
			if id := r["tool_call_id"].(string); id == "ca" || id == "cb" {
				toolOrder = append(toolOrder, id)
			}
		}
	}
	if modes["summary"] == 0 || modes["prune"] == 0 {
		t.Fatalf("前提不成立：应同时发生过裁剪和摘要，实际 %v", modes)
	}
	if len(toolOrder) != 2 || toolOrder[0] != "cb" {
		t.Fatalf("前提不成立：并行工具在日志里应是 b 先于 a（完成顺序），实际 %v", toolOrder)
	}
}

// 专门覆盖“裁剪”的重放：对话在裁剪之后立刻结束，没有后续摘要把被裁剪的消息切掉，
// 所以最终历史里一定还留着被裁剪的那条，重放漏掉裁剪就会和运行时不一致。
func TestReplayReappliesPruneExactly(t *testing.T) {
	prov := funcProvider(func(req agent.Request) agent.Response {
		last := req.Messages[len(req.Messages)-1]
		if last.Role == agent.RoleUser && last.Content == "大输出" {
			return calls("", agent.ToolCall{ID: "ch", Name: "huge", Arguments: "{}"})
		}
		return answer("好的：" + strings.Repeat("字", 30))
	})
	huge := funcTool{name: "huge", run: func(context.Context) (tool.Result, error) {
		return tool.Result{Command: "huge", Stdout: strings.Repeat("大", 900)}, nil
	}}
	dir := t.TempDir()
	rec := NewRecorder(dir, Info{Provider: "p", Model: "m", Cwd: `D:\proj2`}, nil)
	ag, err := agent.New(agent.Config{
		Provider: prov, Model: "m", Recorder: rec, ContextWindow: 2000,
		Tools: registryOf(t, huge), Retry: &agent.RetryPolicy{},
	})
	if err != nil {
		t.Fatal(err)
	}
	var ev kinds
	questions := []string{"大输出"}
	for i := 1; i <= 18; i++ { // 把大输出挤出“最近 6 条”，并推过预算
		questions = append(questions, fmt.Sprintf("短问题 %d", i))
	}
	for _, q := range questions {
		if err := ag.Run(context.Background(), q, &ev); err != nil {
			t.Fatal(err)
		}
	}
	rec.Close()

	modes := map[string]int{}
	for _, r := range readRecords(t, rec.Path()) {
		if r["type"] == "compact" {
			modes[r["mode"].(string)]++
		}
	}
	if modes["prune"] == 0 || modes["summary"] != 0 {
		t.Fatalf("前提不成立：应只发生裁剪、不发生摘要，实际 %v", modes)
	}
	pruned := 0
	for _, m := range ag.History() {
		if m.Role == agent.RoleTool && strings.Contains(m.Content, "旧输出已裁剪") {
			pruned++
		}
	}
	if pruned == 0 {
		t.Fatal("前提不成立：运行时历史里应有被裁剪的工具输出")
	}

	st, err := Load(rec.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(st.History, ag.History()) {
		t.Fatalf("重放没有还原裁剪：\n重放:   %+v\n运行时: %+v", st.History, ag.History())
	}
}
