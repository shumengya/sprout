package agent

import (
	"context"
	"slices"
	"testing"

	"github.com/shumengya/sprout/internal/agent/event"
	"github.com/shumengya/sprout/internal/agent/tool"
)

// 一轮进行中排队的输入在这批工具执行完后接进历史，下一次模型调用就能看到；
// 模型给最终答案的那一步之后不再取，剩下的留给界面作为新的一轮发出。
func TestQueuedInputJoinsAfterToolResults(t *testing.T) {
	p := &scripted{steps: []scriptStep{
		callTools(call("c1", "echo", `{}`)),
		say("收到补充"),
	}}
	ag := newAgent(t, p, registry(t, okTool("echo", tool.Result{Stdout: "ok"})), nil)

	takes := 0
	queued := func() []Input {
		takes++
		if takes == 1 {
			return []Input{{Text: "补充一句"}}
		}
		return nil
	}
	var c collector
	if err := ag.Run(context.Background(), Input{Text: "开始", Queued: queued}, &c); err != nil {
		t.Fatal(err)
	}

	if takes != 1 {
		t.Errorf("只应在工具执行完后取一次，实际取了 %d 次", takes)
	}
	// 事件顺序：工具结果之后、下一次模型调用之前。
	kinds := c.kinds()
	fin, uq, next := slices.Index(kinds, event.KindToolFinished), slices.Index(kinds, event.KindUserQueued), -1
	for i, k := range kinds {
		if k == event.KindMessageCompleted {
			next = i // 最后一次模型调用
		}
	}
	if fin < 0 || uq < 0 || !(fin < uq && uq < next) {
		t.Fatalf("事件顺序不对: %v", kinds)
	}
	if e := c.events[uq].(event.UserQueued); e.Prompt != "补充一句" {
		t.Errorf("UserQueued.Prompt = %q", e.Prompt)
	}
	// 第二次请求带上了这条用户消息，紧跟在工具结果后面。
	req := p.reqs[1].Messages
	if len(req) != 4 || req[2].Role != RoleTool || req[3].Role != RoleUser || req[3].Text() != "补充一句" {
		t.Fatalf("第二次请求的消息不对: %+v", req)
	}
	var roles []Role
	for _, m := range ag.History() {
		roles = append(roles, m.Role)
	}
	if want := []Role{RoleUser, RoleAssistant, RoleTool, RoleUser, RoleAssistant}; !slices.Equal(roles, want) {
		t.Fatalf("历史 = %v，期望 %v", roles, want)
	}
	if n := countKind(c.kinds(), event.KindTurnStarted); n != 1 {
		t.Errorf("排队输入不开始新的一轮，turn.started 出现了 %d 次", n)
	}
}

func countKind(kinds []event.Kind, k event.Kind) int {
	n := 0
	for _, x := range kinds {
		if x == k {
			n++
		}
	}
	return n
}
