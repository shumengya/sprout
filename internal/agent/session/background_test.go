package session

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/event"
	"github.com/shumengya/sprout/internal/agent/tool"
)

// bgFuncTool 是声明可后台的工具替身。只用于测试。
type bgFuncTool struct{ funcTool }

func (bgFuncTool) Background() bool { return true }

// 转入后台的调用落盘为带 background 标记的 tool 记录（占位说明）加一条 background 记录（真正的结果），
// 重放出的历史与运行时逐条一致，事件里也能还原出两段。
func TestReplayReproducesBackgroundCallExactly(t *testing.T) {
	// 后台调用等模型第二次被请求时才结束，历史的形状才是确定的：占位结果 → 模型先答 → 后台结果 → 模型汇总。
	release := make(chan struct{})
	prov := funcProvider(func(req agent.Request) agent.Response {
		last := req.Messages[len(req.Messages)-1]
		switch {
		case last.Role == agent.RoleUser && last.Text() == "开始":
			return calls("", agent.ToolCall{ID: "c1", Name: "slow", Arguments: `{"run_in_background":true}`})
		case last.Role == agent.RoleTool:
			close(release)
			return answer("等后台")
		}
		return answer("汇总")
	})
	slow := bgFuncTool{funcTool{name: "slow", run: func(context.Context) (tool.Result, error) {
		<-release
		return tool.Result{Command: "slow", Stdout: "后台结果"}, nil
	}}}

	rec := NewRecorder(t.TempDir(), Info{Provider: "p", Model: "m", Cwd: `D:\projbg`}, nil)
	ag, err := agent.New(agent.Config{Provider: prov, Model: "m", System: "SYS", Recorder: rec,
		Tools: registryOf(t, slow), Retry: &agent.RetryPolicy{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ag.Run(context.Background(), agent.Input{Text: "开始"}, event.SinkFunc(func(event.Event) {})); err != nil {
		t.Fatal(err)
	}
	rec.Close()

	st, err := Load(rec.Path())
	if err != nil {
		t.Fatal(err)
	}
	if roles(st.History) != "user,assistant,tool,assistant,user,assistant" {
		t.Fatalf("历史 = %s", roles(st.History))
	}
	if !reflect.DeepEqual(st.History, ag.History()) {
		t.Fatalf("历史不一致：\n重放:   %+v\n运行时: %+v", st.History, ag.History())
	}
	var placeholder, done bool
	for _, e := range st.Events {
		switch e := e.(type) {
		case event.ToolFinished:
			placeholder = placeholder || (e.ID == "c1" && e.Background)
		case event.BackgroundFinished:
			done = e.Result.ID == "c1" && e.Result.Stdout == "后台结果" && strings.HasPrefix(e.Message, "[后台任务已结束")
		}
	}
	if !placeholder || !done {
		t.Errorf("重放事件应还原占位与后台结果：placeholder=%v done=%v", placeholder, done)
	}
	if st.Turns != 1 {
		t.Errorf("后台结果不算新的一轮，Turns = %d", st.Turns)
	}
}
