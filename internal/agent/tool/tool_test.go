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

// 桩工具可以附带平台说明，用来验证覆盖描述时说明仍会追加。
type notedTool struct{ stubTool }

func (notedTool) Description() string     { return "内置描述" }
func (n notedTool) PlatformNotes() string { return "当前环境：Git Bash。" }

func TestDecoratorOverridesDescriptionAndParameters(t *testing.T) {
	base := stubTool{"read"}
	schema := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)
	d := Decorator{Tool: base, DescriptionText: "只允许读工作目录下的文件。", ParamSchema: schema}

	if got := d.Description(); got != "只允许读工作目录下的文件。" {
		t.Errorf("Description = %q", got)
	}
	if string(d.Parameters()) != string(schema) {
		t.Errorf("Parameters = %s", d.Parameters())
	}
	// 执行行为不变：Run 直接透传到底层工具。
	if _, err := d.Run(context.Background(), json.RawMessage(`{}`)); err != nil {
		t.Errorf("Run 应透传: %v", err)
	}
}

func TestDecoratorFallsBackWhenEmpty(t *testing.T) {
	base := notedTool{stubTool{"bash"}}
	d := Decorator{Tool: base}
	if d.Description() != "内置描述" {
		t.Errorf("没写 description 时应沿用内置值，得到 %q", d.Description())
	}
	if string(d.Parameters()) != `{}` {
		t.Errorf("没写 parameters 时应沿用内置值，得到 %s", d.Parameters())
	}
}

// 覆盖描述时，平台说明仍然追加：否则用户写死的描述会让模型在 PowerShell 5.1 上用 &&。
func TestDecoratorKeepsPlatformNotes(t *testing.T) {
	d := Decorator{Tool: notedTool{stubTool{"powershell"}}, DescriptionText: "执行 PowerShell 命令。"}
	if got := d.Description(); got != "执行 PowerShell 命令。\n当前环境：Git Bash。" {
		t.Errorf("Description = %q", got)
	}
}

type serialStub struct{ stubTool }

func (serialStub) Parallel() bool { return false }

func TestParallelDeclaration(t *testing.T) {
	no, yes := false, true
	cases := []struct {
		name string
		t    Tool
		want bool
	}{
		{"未声明按可并行", stubTool{"a"}, true},
		{"声明不可并行", serialStub{stubTool{"a"}}, false},
		{"Decorator 透传声明", Decorator{Tool: serialStub{stubTool{"a"}}}, false},
		{"Decorator 透传未声明", Decorator{Tool: stubTool{"a"}}, true},
		{"配置覆盖成可并行", Decorator{Tool: serialStub{stubTool{"a"}}, ParallelOverride: &yes}, true},
		{"配置覆盖成不可并行", Decorator{Tool: stubTool{"a"}, ParallelOverride: &no}, false},
	}
	for _, c := range cases {
		if got := Parallel(c.t); got != c.want {
			t.Errorf("%s: Parallel = %v，期望 %v", c.name, got, c.want)
		}
	}
}

type bgStub struct{ stubTool }

func (bgStub) Background() bool { return true }

type serialBGStub struct{ bgStub }

func (serialBGStub) Parallel() bool { return false }

func TestBackgroundDeclaration(t *testing.T) {
	no, yes := false, true
	cases := []struct {
		name string
		t    Tool
		want bool
	}{
		{"未声明不可后台", stubTool{"a"}, false},
		{"声明可后台", bgStub{stubTool{"a"}}, true},
		// 独占执行的承诺和后台执行冲突：声明了也不算。
		{"不可并行的不能后台", serialBGStub{bgStub{stubTool{"a"}}}, false},
		{"Decorator 透传声明", Decorator{Tool: bgStub{stubTool{"a"}}}, true},
		{"Decorator 透传未声明", Decorator{Tool: stubTool{"a"}}, false},
		{"配置打开后台", Decorator{Tool: stubTool{"a"}, BackgroundOverride: &yes}, true},
		{"配置关闭后台", Decorator{Tool: bgStub{stubTool{"a"}}, BackgroundOverride: &no}, false},
		{"配置打开后台但不可并行", Decorator{Tool: stubTool{"a"}, BackgroundOverride: &yes, ParallelOverride: &no}, false},
	}
	for _, c := range cases {
		if got := Background(c.t); got != c.want {
			t.Errorf("%s: Background = %v，期望 %v", c.name, got, c.want)
		}
	}
}
