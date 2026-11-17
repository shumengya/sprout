package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent/event"
)

// 主会话只记子代理的用量（subagent_usage），其他子代理事件不落进主会话；
// 恢复时用量还原成 event.Subagent，而且不打乱 agent 工具调用的历史。
func TestSubagentUsageRecordedAndReplayed(t *testing.T) {
	r, _ := newRec(t)
	r.Emit(event.TurnStarted{Prompt: "q"})
	r.Emit(event.MessageCompleted{ToolCalls: []event.ToolCall{{ID: "call_1", Name: "agent", Arguments: `{"prompt":"p"}`}}})
	r.Emit(event.ToolRequested{Call: event.ToolCall{ID: "call_1", Name: "agent"}})
	r.Emit(event.Subagent{CallID: "call_1", Agent: "explore", Event: event.TurnStarted{Prompt: "p"}})
	r.Emit(event.Subagent{CallID: "call_1", Agent: "explore", Event: event.ToolFinished{ID: "x", Name: "read", Stdout: "子代理读到的内容"}})
	r.Emit(event.Subagent{CallID: "call_1", Agent: "explore", Event: event.Usage{InputTokens: 100, OutputTokens: 7, TotalTokens: 107, CacheReadTokens: 40}})
	r.Emit(event.ToolFinished{ID: "call_1", Name: "agent", Stdout: "报告", Content: "报告"})
	r.Emit(event.MessageCompleted{Content: "好了"})
	r.Emit(event.TurnFinished{Reason: event.ReasonStop})
	r.Close()

	var types []string
	for _, rec := range readRecords(t, r.Path()) {
		types = append(types, rec["type"].(string))
		if rec["type"] == "subagent_usage" && (rec["tool_call_id"] != "call_1" || rec["agent"] != "explore" || rec["input_tokens"] != float64(100) || rec["cache_read_tokens"] != float64(40)) {
			t.Errorf("subagent_usage = %v", rec)
		}
	}
	if got := strings.Join(types, ","); got != "session,user,assistant,subagent_usage,tool,assistant,turn" {
		t.Errorf("记录类型 = %s", got)
	}
	if b, _ := os.ReadFile(r.Path()); strings.Contains(string(b), "子代理读到的内容") {
		t.Error("子代理的工具输出不该写进主会话")
	}

	st, err := Load(r.Path())
	if err != nil {
		t.Fatal(err)
	}
	var sub []event.Subagent
	for _, e := range st.Events {
		if s, ok := e.(event.Subagent); ok {
			sub = append(sub, s)
		}
	}
	if len(sub) != 1 || sub[0].CallID != "call_1" || sub[0].Agent != "explore" || sub[0].Event.(event.Usage).OutputTokens != 7 {
		t.Fatalf("重放出的子代理用量 = %+v", sub)
	}
	if len(st.History) != 4 || st.History[2].Text() != "报告" {
		t.Errorf("历史 = %+v", st.History)
	}
}

func TestChildRecorderWritesFixedPathOutsideIndex(t *testing.T) {
	r, root := newRec(t)
	r.Emit(event.TurnStarted{Prompt: "q"})
	parent := r.Path()

	p := ChildPath(parent, "call:1/../x")
	if filepath.Dir(p) != strings.TrimSuffix(parent, ".jsonl")+".agents" || filepath.Base(p) != "call_1____x.jsonl" {
		t.Fatalf("ChildPath = %s", p)
	}
	info := testInfo
	info.Agent = "explore"
	c := NewChildRecorder(p, info, nil)
	c.Emit(event.TurnStarted{Prompt: "子任务"})
	c.Emit(event.MessageCompleted{Content: "子报告"})
	c.Emit(event.TurnFinished{Reason: event.ReasonStop})
	c.Close()

	st, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Info.Agent != "explore" || len(st.History) != 2 || st.History[1].Text() != "子报告" {
		t.Errorf("子代理记录 = %+v", st)
	}
	list, _ := List(root, testInfo.Cwd)
	if len(list) != 1 || list[0].Path != parent {
		t.Errorf("子代理记录不该出现在会话列表里: %+v", list)
	}

	// 同一路径不覆盖：已有文件时停止记录，原内容不变。
	again := NewChildRecorder(p, info, nil)
	again.Emit(event.TurnStarted{Prompt: "另一个"})
	if again.Err() == nil {
		t.Error("目标已存在时应报错并停止记录")
	}
	if b, _ := os.ReadFile(p); strings.Contains(string(b), "另一个") {
		t.Error("不能覆盖已有的子代理记录")
	}
}
