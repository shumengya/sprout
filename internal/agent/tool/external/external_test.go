package external

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent/platform"
)

// findBash 返回本机可用的 bash，找不到时跳过用例。
// 测试要求真实 shell：这里验证的正是引号与转义在真实 shell 下的行为，替身证明不了。
func findBash(t *testing.T) platform.Shell {
	t.Helper()
	for _, p := range []string{"bash", "C:/Program Files/Git/bin/bash.exe"} {
		if abs, err := exec.LookPath(p); err == nil {
			return platform.Shell{Kind: platform.KindBash, Path: abs, Label: "bash"}
		}
	}
	t.Skip("本机没有 bash")
	return platform.Shell{}
}

func run(t *testing.T, tl *Tool, args map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	res, err := tl.Run(context.Background(), raw)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	return res.Stdout
}

// 参数里带空格和单引号时，必须原样作为一个整体传给命令，不能被 shell 拆开或吃掉。
func TestBashQuotesArguments(t *testing.T) {
	sh := findBash(t)
	tl := New(Spec{
		Name: "echo", Description: "回显参数",
		Parameters: json.RawMessage(`{"type":"object","properties":{"msg":{"type":"string"}},"required":["msg"]}`),
		Shell:      "bash", Command: `printf '%s' {{msg}}`,
	}, t.TempDir(), sh)

	cases := []string{
		"hello world",
		"it's here",
		`a'b"c`,
		"$(whoami)",
		"中文 路径 带 空格",
	}
	for _, want := range cases {
		if got := run(t, tl, map[string]any{"msg": want}); got != want {
			t.Errorf("输入 %q，输出 %q", want, got)
		}
	}
}

func TestBashRunsInWorkingDir(t *testing.T) {
	sh := findBash(t)
	tl := New(Spec{Name: "pwd", Description: "打印工作目录", Shell: "bash", Command: "pwd"}, t.TempDir(), sh)
	// Windows 上 Git Bash 输出 /d/... 形式，用 EvalSymlinks 反解后比较结尾即可。
	got := strings.TrimSpace(run(t, tl, nil))
	if !strings.HasSuffix(strings.ToLower(filepath.ToSlash(got)), strings.ToLower(filepath.Base(tl.dir))) {
		t.Errorf("pwd = %q，工作目录应为 %q", got, tl.dir)
	}
}

func TestMissingRequiredArgument(t *testing.T) {
	tl := New(Spec{
		Name: "x", Description: "d", Shell: "bash", Command: "echo {{msg}}",
		Parameters: json.RawMessage(`{"type":"object","properties":{"msg":{"type":"string"}},"required":["msg"]}`),
	}, t.TempDir(), platform.Shell{Path: "/nonexistent"})
	if _, err := tl.Run(context.Background(), json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "msg") {
		t.Errorf("缺少必填参数应报错并点名，得到 %v", err)
	}
}

func TestMissingShellIsReported(t *testing.T) {
	tl := New(Spec{Name: "x", Description: "d", Shell: "bash", Command: "echo hi"}, t.TempDir(), platform.Shell{})
	if _, err := tl.Run(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "bash") {
		t.Errorf("没有 shell 时应报错，得到 %v", err)
	}
}

// direct 模式不经 shell：参数里的 $ 与空格不会被解释。
func TestDirectRunsWithoutShell(t *testing.T) {
	echo := "echo"
	if runtime.GOOS == "windows" {
		echo = "cmd"
	}
	spec := Spec{Name: "say", Description: "回显", Shell: "direct", Command: echo}
	if runtime.GOOS == "windows" {
		spec.Args = []string{"/c", "echo", "{{msg}}"}
	} else {
		spec.Args = []string{"{{msg}}"}
	}
	tl := New(spec, t.TempDir(), platform.Shell{})
	got := strings.TrimSpace(run(t, tl, map[string]any{"msg": "$HOME 与 空格"}))
	if !strings.Contains(got, "$HOME") {
		t.Errorf("direct 模式不应展开变量，输出 %q", got)
	}
}

func TestDirectSubstitutesArgsAndCommand(t *testing.T) {
	tl := New(Spec{
		Name: "x", Description: "d",
		Shell: "direct", Command: "{{bin}}", Args: []string{"{{a}}", "{{b}}"},
	}, t.TempDir(), platform.Shell{})
	opts, err := tl.command(json.RawMessage(`{"bin":"/usr/bin/env","a":"1","b":"2"}`))
	if err != nil {
		t.Fatal(err)
	}
	if opts.Path != "/usr/bin/env" || strings.Join(opts.Args, ",") != "1,2" {
		t.Errorf("opts = %+v", opts)
	}
}

func TestCommandShorthandSplitsOnSpaces(t *testing.T) {
	tl := New(Spec{Name: "x", Description: "d", Shell: "direct", Command: "git status --short"}, t.TempDir(), platform.Shell{})
	opts, err := tl.command(nil)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Path != "git" || strings.Join(opts.Args, " ") != "status --short" {
		t.Errorf("opts = %+v", opts)
	}
}

func TestDefaultsParametersToEmptyObject(t *testing.T) {
	tl := New(Spec{Name: "x", Description: "d", Shell: "direct", Command: "true"}, t.TempDir(), platform.Shell{})
	var schema map[string]any
	if err := json.Unmarshal(tl.Parameters(), &schema); err != nil || schema["type"] != "object" {
		t.Errorf("默认参数 schema = %s", tl.Parameters())
	}
	if tl.Description() != "d" || tl.Name() != "x" {
		t.Error("名字与描述应来自配置")
	}
}

func TestShellOptionsUseNonInteractiveFlags(t *testing.T) {
	tl := New(Spec{Name: "x", Description: "d", Shell: "powershell", Command: "Get-Date"}, t.TempDir(),
		platform.Shell{Path: "pwsh.exe"})
	opts := tl.shellOptions("Get-Date")
	if !strings.Contains(strings.Join(opts.Args, " "), "-NoProfile") || !strings.Contains(opts.Args[len(opts.Args)-1], "Get-Date") {
		t.Errorf("args = %v", opts.Args)
	}
}
