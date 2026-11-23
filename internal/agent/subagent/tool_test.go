package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/event"
	"github.com/shumengya/sprout/internal/agent/tool"
)

type stubTool struct{ name string }

func (s stubTool) Name() string                { return s.name }
func (s stubTool) Description() string         { return s.name }
func (s stubTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s stubTool) Run(context.Context, json.RawMessage) (tool.Result, error) {
	return tool.Result{}, nil
}

// scriptRunner 把预先写好的事件发给 sink，模拟子代理的一轮。只用于测试。
type scriptRunner struct {
	events []event.Event
	err    error
	gotIn  agent.Input
	hook   func(ctx context.Context)
}

func (r *scriptRunner) Run(ctx context.Context, in agent.Input, sink event.Sink) error {
	r.gotIn = in
	if r.hook != nil {
		r.hook(ctx)
	}
	for _, e := range r.events {
		sink.Emit(e)
	}
	return r.err
}

func parentRegistry(t *testing.T, names ...string) *tool.Registry {
	t.Helper()
	reg := tool.NewRegistry()
	for _, n := range names {
		if err := reg.Register(stubTool{n}); err != nil {
			t.Fatal(err)
		}
	}
	return reg
}

func runAgent(t *testing.T, tl *Tool, args string) (tool.Result, error, []event.Event) {
	t.Helper()
	var forwarded []event.Event
	ctx := tool.WithCall(context.Background(), tool.Call{ID: "call_9", Emit: func(e event.Event) { forwarded = append(forwarded, e) }})
	res, err := tl.Run(ctx, json.RawMessage(args))
	return res, err, forwarded
}

func finished(text string) []event.Event {
	return []event.Event{
		event.TurnStarted{Prompt: "p"},
		event.MessageDelta{Text: text},
		event.MessageCompleted{Content: "先看看", ToolCalls: []event.ToolCall{{ID: "c1", Name: "read"}}},
		event.Usage{InputTokens: 10, OutputTokens: 3},
		event.ToolRequested{Call: event.ToolCall{ID: "c1", Name: "read"}},
		event.ToolFinished{ID: "c1", Name: "read"},
		event.MessageCompleted{Content: text},
		event.Usage{InputTokens: 20, OutputTokens: 5},
		event.TurnFinished{Reason: event.ReasonStop},
	}
}

func TestRunFiltersToolsForwardsEventsAndReturnsReport(t *testing.T) {
	reg := parentRegistry(t, "bash", "read", "write", "mcp__gh__x")
	r := &scriptRunner{events: finished("结论：在 a.go:3")}
	var got Request
	tl := New(reg, Builtins(), func(req Request) (*Child, error) {
		got = req
		return &Child{Agent: r}, nil
	})
	if err := reg.Register(tl); err != nil {
		t.Fatal(err)
	}

	res, err, fwd := runAgent(t, tl, `{"description":"查  一下","prompt":"  找出 X  ","agent":"explore"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stdout != "结论：在 a.go:3" || res.ExitCode != 0 || res.Command != "explore: 查 一下" {
		t.Errorf("res = %+v", res)
	}
	if r.gotIn.Text != "找出 X" || got.CallID != "call_9" || got.Def.Name != "explore" {
		t.Errorf("input=%q req=%+v", r.gotIn.Text, got)
	}
	if names := got.Tools.Names(); !slices.Equal(names, []string{"bash", "read"}) {
		t.Errorf("explore 只该拿到 read 和 shell，且永远不含 agent: %v", names)
	}
	// 增量不转发，其余原样包成 Subagent，CallID / Agent 正确。
	if len(fwd) != len(r.events)-1 {
		t.Fatalf("转发了 %d 个事件，期望 %d", len(fwd), len(r.events)-1)
	}
	for _, e := range fwd {
		s, ok := e.(event.Subagent)
		if !ok || s.CallID != "call_9" || s.Agent != "explore" {
			t.Fatalf("转发的事件 = %#v", e)
		}
		if _, isDelta := s.Event.(event.MessageDelta); isDelta {
			t.Fatal("增量不该转发")
		}
	}
}

func TestRunGeneralGetsAllToolsExceptAgent(t *testing.T) {
	reg := parentRegistry(t, "bash", "write")
	var got Request
	tl := New(reg, Builtins(), func(req Request) (*Child, error) {
		got = req
		return &Child{Agent: &scriptRunner{events: finished("ok")}}, nil
	})
	_ = reg.Register(tl)
	if _, err, _ := runAgent(t, tl, `{"description":"d","prompt":"p"}`); err != nil {
		t.Fatal(err)
	}
	if got.Def.Name != DefaultAgent || !slices.Equal(got.Tools.Names(), []string{"bash", "write"}) {
		t.Errorf("def=%s tools=%v", got.Def.Name, got.Tools.Names())
	}
}

func TestRunRejectsBadArguments(t *testing.T) {
	tl := New(parentRegistry(t, "read"), Builtins(), func(Request) (*Child, error) {
		t.Fatal("参数无效时不该派生子代理")
		return nil, nil
	})
	for args, want := range map[string]string{
		`{"description":"d","prompt":"  "}`:            "prompt 不能为空",
		`{"description":"d","prompt":"p","agent":"x"}`: "可选",
		`not json`: "JSON",
	} {
		if _, err, _ := runAgent(t, tl, args); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v，期望包含 %q", args, err, want)
		}
	}
}

func TestRunNoAllowedToolsAvailable(t *testing.T) {
	defs := []Definition{{Name: "only-mcp", Description: "d", Tools: []string{"mcp__*"}}}
	tl := New(parentRegistry(t, "read"), defs, func(Request) (*Child, error) { return nil, errors.New("不该调用") })
	if _, err, _ := runAgent(t, tl, `{"description":"d","prompt":"p","agent":"only-mcp"}`); err == nil || !strings.Contains(err.Error(), "一个都不可用") {
		t.Errorf("err = %v", err)
	}
}

func TestRunReportsChildFailures(t *testing.T) {
	cases := []struct {
		name   string
		events []event.Event
		runErr error
		want   []string
	}{
		{"出错", []event.Event{
			event.MessageCompleted{Content: "查到一半", ToolCalls: []event.ToolCall{{ID: "c"}}},
			event.Error{Message: "429 限流"}, event.TurnFinished{Reason: event.ReasonError},
		}, errors.New("429"), []string{"子代理出错", "429 限流", "查到一半"}},
		{"截断", []event.Event{
			event.MessageCompleted{Content: "写了一半的报告"}, event.TurnFinished{Reason: event.ReasonLength},
		}, nil, []string{"长度上限", "写了一半的报告"}},
		{"没有文字", []event.Event{event.MessageCompleted{}, event.TurnFinished{Reason: event.ReasonStop}}, nil,
			[]string{"没有给出文字报告"}},
	}
	for _, c := range cases {
		tl := New(parentRegistry(t, "read"), Builtins(), func(Request) (*Child, error) {
			return &Child{Agent: &scriptRunner{events: c.events, err: c.runErr}}, nil
		})
		res, err, _ := runAgent(t, tl, `{"description":"d","prompt":"p"}`)
		if err != nil || res.ExitCode != 1 {
			t.Errorf("%s: 子代理失败应作为退出码 1 的结果交回，err=%v res=%+v", c.name, err, res)
		}
		for _, w := range c.want {
			if !strings.Contains(res.Stdout, w) {
				t.Errorf("%s: 报告缺少 %q: %q", c.name, w, res.Stdout)
			}
		}
	}
}

func TestRunCancelledReturnsContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	closed := false
	r := &scriptRunner{
		hook:   func(context.Context) { cancel() },
		events: []event.Event{event.Interrupted{Reason: event.ReasonCancelled, Content: "半截"}, event.TurnFinished{Reason: event.ReasonCancelled}},
		err:    context.Canceled,
	}
	tl := New(parentRegistry(t, "read"), Builtins(), func(Request) (*Child, error) {
		return &Child{Agent: r, Close: func() { closed = true }}, nil
	})
	res, err := tl.Run(ctx, json.RawMessage(`{"description":"d","prompt":"p"}`))
	if !errors.Is(err, context.Canceled) || !strings.Contains(res.Stdout, "半截") || res.Command == "" {
		t.Errorf("err=%v res=%+v", err, res)
	}
	if !closed {
		t.Error("子代理结束后必须调用 Close（关闭记录文件）")
	}
}

func TestReportIsClipped(t *testing.T) {
	long := strings.Repeat("长", maxReport) // 每个字 3 字节，远超上限
	tl := New(parentRegistry(t, "read"), Builtins(), func(Request) (*Child, error) {
		return &Child{Agent: &scriptRunner{events: []event.Event{event.MessageCompleted{Content: long}, event.TurnFinished{Reason: event.ReasonStop}}}}, nil
	})
	res, _, _ := runAgent(t, tl, `{"description":"d","prompt":"p"}`)
	if !res.Truncated || len(res.Stdout) > maxReport+100 || !strings.Contains(res.Stdout, "已截断") {
		t.Errorf("truncated=%v len=%d", res.Truncated, len(res.Stdout))
	}
	if !strings.HasPrefix(res.Stdout, "长长") || !utf8.ValidString(res.Stdout) {
		t.Error("应按字符边界截断")
	}
}

func TestDescriptionAndSchemaListTypes(t *testing.T) {
	defs := append(Builtins(), Definition{Name: "reviewer", Description: "审查改动", Tools: []string{}})
	tl := New(tool.NewRegistry(), defs, nil)
	for _, want := range []string{"general：", "explore：", "工具：read、bash、powershell", "reviewer：审查改动（没有工具）", "不能再派子代理"} {
		if !strings.Contains(tl.Description(), want) {
			t.Errorf("描述缺少 %q", want)
		}
	}
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(tl.Parameters(), &schema); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(schema.Properties["agent"].Enum, []string{"general", "explore", "reviewer"}) || !slices.Contains(schema.Required, "prompt") {
		t.Errorf("schema = %+v", schema)
	}
}

func TestSystemSection(t *testing.T) {
	s := SystemSection(Definition{Name: "explore", Prompt: "只读"})
	for _, want := range []string{"## 子代理", "类型：explore", "不能向用户", "### 角色\n只读"} {
		if !strings.Contains(s, want) {
			t.Errorf("缺少 %q: %s", want, s)
		}
	}
	if strings.Contains(SystemSection(Definition{Name: "x"}), "### 角色") {
		t.Error("没有角色说明时不输出空标题")
	}
}

// agent 工具声明可后台：子代理跑得久，主代理可以先派出去做别的。开关参数由内核加，不在工具自己的 schema 里。
func TestAgentToolIsBackgroundable(t *testing.T) {
	tl := New(tool.NewRegistry(), Builtins(), nil)
	if !tool.Background(tl) {
		t.Fatal("agent 工具应声明可后台")
	}
	if strings.Contains(string(tl.Parameters()), tool.BackgroundParam) {
		t.Error("开关参数由内核注入，工具自己的 schema 里不该有")
	}
	if !strings.Contains(tl.Description(), "run_in_background") {
		t.Error("描述里应说明什么时候放到后台")
	}
}
