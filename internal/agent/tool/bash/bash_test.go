package bash

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shumengya/sprout/internal/agent/platform"
)

// realBash 返回真实可用的 bash 工具；本机没有 bash 时跳过，不用替身冒充。
func realBash(t *testing.T) (*Tool, string) {
	t.Helper()
	sh := platform.DetectShells("", "").Bash
	if sh == nil {
		t.Skip("本机未探测到可用的 bash")
	}
	dir := t.TempDir()
	return New(*sh, dir), dir
}

func args(command string, timeout int) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"command": command, "timeout_seconds": timeout})
	return b
}

func TestBashRunsRealCommand(t *testing.T) {
	tl, _ := realBash(t)
	res, err := tl.Run(context.Background(), args("echo hello; echo oops >&2; exit 4", 0))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(res.Stdout) != "hello" || strings.TrimSpace(res.Stderr) != "oops" || res.ExitCode != 4 {
		t.Fatalf("得到 %+v", res)
	}
	if res.Command != "echo hello; echo oops >&2; exit 4" {
		t.Errorf("Command 应回显原命令，得到 %q", res.Command)
	}
}

func TestBashChineseOutput(t *testing.T) {
	tl, _ := realBash(t)
	res, err := tl.Run(context.Background(), args("echo 你好，世界", 0))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(res.Stdout); got != "你好，世界" {
		t.Fatalf("中文输出得到 %q", got)
	}
}

func TestBashWorkingDirAndNoStateAcrossCalls(t *testing.T) {
	tl, dir := realBash(t)
	if _, err := tl.Run(context.Background(), args("cd / ; export SPROUT_X=1", 0)); err != nil {
		t.Fatal(err)
	}
	res, err := tl.Run(context.Background(), args("echo \"[$SPROUT_X]\"; pwd", 0))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Stdout, "[]") {
		t.Errorf("环境变量不应跨调用保留，得到 %q", res.Stdout)
	}
	// 工作目录是启动目录而不是上一次 cd 的结果；Git Bash 的 pwd 会把盘符转成 /c/ 形式，只比较末段目录名。
	base := dir[strings.LastIndexAny(dir, `/\`)+1:]
	if !strings.Contains(res.Stdout, base) {
		t.Errorf("pwd 应在 %q 下，得到 %q", dir, res.Stdout)
	}
}

func TestBashTimeout(t *testing.T) {
	tl, _ := realBash(t)
	start := time.Now()
	res, err := tl.Run(context.Background(), args("sleep 30", 1))
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Fatalf("应超时，得到 %+v", res)
	}
	if time.Since(start) > 8*time.Second {
		t.Fatalf("超时后耗时 %v，进程树可能没被终止", time.Since(start))
	}
}

func TestBashStdinIsEmpty(t *testing.T) {
	tl, _ := realBash(t)
	res, err := tl.Run(context.Background(), args("cat | wc -c", 10))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(res.Stdout) != "0" || res.TimedOut {
		t.Fatalf("cat 应立即读到 EOF，得到 %+v", res)
	}
}

func TestBashTruncatesLargeOutput(t *testing.T) {
	tl, _ := realBash(t)
	res, err := tl.Run(context.Background(), args("head -c 200000 /dev/zero | tr '\\0' 'x'", 30))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || len(res.Stdout) > 50*1024 {
		t.Fatalf("Truncated=%v，stdout %d 字节", res.Truncated, len(res.Stdout))
	}
}

func TestBashInvalidArgs(t *testing.T) {
	tl, _ := realBash(t)
	if _, err := tl.Run(context.Background(), json.RawMessage(`{"command":""}`)); err == nil {
		t.Fatal("空命令应返回 error")
	}
}

func TestBashDescription(t *testing.T) {
	win := New(platform.Shell{Kind: platform.KindBash, Label: "Git Bash", Notes: "路径使用 POSIX 形式（如 /d/foo）"}, "")
	d := win.Description()
	for _, want := range []string{"Git Bash", "POSIX", "全新的进程", "stdin"} {
		if !strings.Contains(d, want) {
			t.Errorf("描述缺少 %q: %s", want, d)
		}
	}
	unix := New(platform.Shell{Kind: platform.KindBash, Label: "bash"}, "")
	if strings.Contains(unix.Description(), "POSIX") {
		t.Error("非 Windows 的 bash 描述不应带 Windows 路径说明")
	}
	if win.Name() != "bash" {
		t.Errorf("Name = %q", win.Name())
	}
	var schema map[string]any
	if err := json.Unmarshal(win.Parameters(), &schema); err != nil {
		t.Errorf("Parameters 不是合法 JSON: %v", err)
	}
}
