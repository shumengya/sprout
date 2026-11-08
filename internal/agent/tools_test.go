package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shumengya/sprout/internal/agent/event"
	"github.com/shumengya/sprout/internal/agent/tool"
)

// 工具层面的任何失败都应该变成 tool 消息回给模型，而不是终止 agent。
func runWithTool(t *testing.T, tl tool.Tool, toolName string) (event.ToolFinished, Message, *scripted) {
	t.Helper()
	p := &scripted{steps: []scriptStep{callTools(call("c1", toolName, `{"command":"x"}`)), say("好")}}
	a := newAgent(t, p, registry(t, tl), nil)
	var c collector
	if err := a.Run(context.Background(), "q", &c); err != nil {
		t.Fatalf("工具失败不应终止 agent: %v", err)
	}
	var fin event.ToolFinished
	for _, e := range c.events {
		if f, ok := e.(event.ToolFinished); ok {
			fin = f
		}
	}
	return fin, p.reqs[1].Messages[2], p
}

func TestUnknownToolIsReportedToModel(t *testing.T) {
	fin, msg, _ := runWithTool(t, okTool("bash", tool.Result{}), "rm_rf")
	if !strings.Contains(fin.Err, "未知工具") || !strings.Contains(fin.Err, "bash") {
		t.Errorf("Err = %q，应说明未知并列出可用工具", fin.Err)
	}
	if !strings.Contains(msg.Content, "未知工具") || msg.ToolID != "c1" {
		t.Errorf("tool 消息 = %+v", msg)
	}
}

func TestToolWithoutRegistry(t *testing.T) {
	p := &scripted{steps: []scriptStep{callTools(call("c1", "bash", `{}`)), say("好")}}
	a := newAgent(t, p, nil, nil)
	if err := a.Run(context.Background(), "q", &collector{}); err != nil {
		t.Fatal(err)
	}
	if got := p.reqs[1].Messages[2].Content; !strings.Contains(got, "未知工具") || !strings.Contains(got, "（无）") {
		t.Errorf("没有任何工具时的提示不对: %q", got)
	}
}

func TestToolStartFailureIsReportedToModel(t *testing.T) {
	failing := fakeTool{name: "bash", run: func(context.Context, json.RawMessage) (tool.Result, error) {
		return tool.Result{}, errors.New("无法启动 bash: 文件不存在")
	}}
	fin, msg, _ := runWithTool(t, failing, "bash")
	if !strings.Contains(fin.Err, "无法启动") || fin.Cancelled {
		t.Errorf("ToolFinished = %+v", fin)
	}
	if !strings.HasPrefix(msg.Content, "工具执行失败: ") {
		t.Errorf("tool 消息 = %q", msg.Content)
	}
}

func TestNonZeroExitAndTimeoutReachTheModel(t *testing.T) {
	res := tool.Result{Command: "make", Stderr: "boom", ExitCode: 2, TimedOut: true, Truncated: true, Duration: 3 * time.Second}
	fin, msg, _ := runWithTool(t, okTool("bash", res), "bash")
	if fin.Err != "" || fin.ExitCode != 2 || !fin.TimedOut || !fin.Truncated || fin.Duration != 3*time.Second {
		t.Errorf("ToolFinished = %+v", fin)
	}
	for _, want := range []string{"exit_code: 2", "timed_out: true", "truncated: true", "boom"} {
		if !strings.Contains(msg.Content, want) {
			t.Errorf("tool 消息缺少 %q:\n%s", want, msg.Content)
		}
	}
}

func TestToolDurationFallsBackToWallClock(t *testing.T) {
	slow := fakeTool{name: "bash", run: func(context.Context, json.RawMessage) (tool.Result, error) {
		time.Sleep(20 * time.Millisecond)
		return tool.Result{Command: "x"}, nil
	}}
	fin, _, _ := runWithTool(t, slow, "bash")
	if fin.Duration < 20*time.Millisecond {
		t.Errorf("工具没给耗时时应使用墙钟时间，得到 %v", fin.Duration)
	}
}

func TestToolSeesRawArguments(t *testing.T) {
	var got string
	spy := fakeTool{name: "bash", run: func(_ context.Context, a json.RawMessage) (tool.Result, error) {
		got = string(a)
		return tool.Result{}, nil
	}}
	runWithTool(t, spy, "bash")
	if got != `{"command":"x"}` {
		t.Errorf("工具应收到模型给出的原始参数，得到 %q", got)
	}
}

func TestToolCallsRunSerially(t *testing.T) {
	var order []string
	mk := func(name string) fakeTool {
		return fakeTool{name: name, run: func(context.Context, json.RawMessage) (tool.Result, error) {
			order = append(order, name)
			return tool.Result{}, nil
		}}
	}
	p := &scripted{steps: []scriptStep{
		callTools(call("1", "a", `{}`), call("2", "b", `{}`), call("3", "a", `{}`)),
		say("ok"),
	}}
	a := newAgent(t, p, registry(t, mk("a"), mk("b")), nil)
	if err := a.Run(context.Background(), "q", &collector{}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, "") != "aba" {
		t.Errorf("执行顺序 = %v", order)
	}
	msgs := p.reqs[1].Messages
	if len(msgs) != 5 || msgs[2].ToolID != "1" || msgs[3].ToolID != "2" || msgs[4].ToolID != "3" {
		t.Errorf("tool 结果应按调用顺序回传: %+v", msgs)
	}
}

func TestClipKeepsValidUTF8(t *testing.T) {
	if got := clip("你好世界", 2); got != "你好…" {
		t.Errorf("clip = %q", got)
	}
	if got := clip("short", 10); got != "short" {
		t.Errorf("clip = %q", got)
	}
}
