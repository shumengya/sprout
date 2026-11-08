package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type stubTool struct{ name string }

func (s stubTool) Name() string                { return s.name }
func (s stubTool) Description() string         { return "" }
func (s stubTool) Parameters() json.RawMessage { return json.RawMessage(`{}`) }
func (s stubTool) Run(context.Context, json.RawMessage) (Result, error) {
	return Result{}, nil
}

func TestRegistryOrderAndDuplicate(t *testing.T) {
	r := NewRegistry()
	for _, n := range []string{"powershell", "bash"} {
		if err := r.Register(stubTool{n}); err != nil {
			t.Fatalf("注册 %s 失败: %v", n, err)
		}
	}
	if got := strings.Join(r.Names(), ","); got != "powershell,bash" {
		t.Fatalf("Names = %q，应保持注册顺序", got)
	}
	if err := r.Register(stubTool{"bash"}); err == nil {
		t.Fatal("重复注册应返回错误")
	}
	if err := r.Register(stubTool{""}); err == nil {
		t.Fatal("空名称应返回错误")
	}
	if _, ok := r.Get("bash"); !ok {
		t.Fatal("Get(bash) 应存在")
	}
	if _, ok := r.Get("nope"); ok {
		t.Fatal("Get(nope) 不应存在")
	}
}

func TestResultForModel(t *testing.T) {
	out := Result{
		Command:   "ls",
		Stdout:    "a\nb",
		Stderr:    "warn",
		ExitCode:  2,
		Truncated: true,
		TimedOut:  true,
		Duration:  1500 * time.Millisecond,
	}.ForModel()

	for _, want := range []string{
		"command: ls", "exit_code: 2", "truncated: true", "timed_out: true",
		"--- stdout ---\na\nb\n", "--- stderr ---\nwarn\n", "1.5s",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q:\n%s", want, out)
		}
	}
}

func TestResultForModelNoOutput(t *testing.T) {
	out := Result{Command: "true"}.ForModel()
	if !strings.Contains(out, "(无输出)") || strings.Contains(out, "timed_out") {
		t.Fatalf("无输出结果格式不对:\n%s", out)
	}
}

func TestResultForModelTruncatesLongCommand(t *testing.T) {
	out := Result{Command: strings.Repeat("x", 2000)}.ForModel()
	if len(out) > 1000 {
		t.Fatalf("长命令回显未被截断，长度 %d", len(out))
	}
}
