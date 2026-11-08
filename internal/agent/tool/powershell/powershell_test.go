package powershell

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shumengya/sprout/internal/agent/platform"
)

// editions 返回本机真实可用的 PowerShell：自动探测到的一个，加上固定路径的 5.1（若存在）。
// 两个版本都测，因为 5.1 的编码和语法行为与 7+ 不同。
func editions(t *testing.T) map[string]platform.Shell {
	t.Helper()
	out := map[string]platform.Shell{}
	if sh := platform.DetectShells("", "").PowerShell; sh != nil {
		out[sh.Edition] = *sh
	}
	if root := os.Getenv("SystemRoot"); root != "" {
		p := filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			out["5.1"] = platform.Shell{Kind: platform.KindPowerShell, Path: p, Label: "PowerShell 5.1", Edition: "5.1"}
		}
	}
	if len(out) == 0 {
		t.Skip("本机未探测到可用的 PowerShell")
	}
	return out
}

func args(command string, timeout int) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"command": command, "timeout_seconds": timeout})
	return b
}

func eachEdition(t *testing.T, fn func(t *testing.T, tl *Tool, ed string)) {
	for ed, sh := range editions(t) {
		t.Run("v"+ed, func(t *testing.T) { fn(t, New(sh, t.TempDir()), ed) })
	}
}

func TestPowerShellRunsRealCommand(t *testing.T) {
	eachEdition(t, func(t *testing.T, tl *Tool, _ string) {
		res, err := tl.Run(context.Background(), args("Write-Output hello; exit 7", 0))
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(res.Stdout) != "hello" || res.ExitCode != 7 {
			t.Fatalf("得到 %+v", res)
		}
	})
}

func TestPowerShellChineseOutput(t *testing.T) {
	eachEdition(t, func(t *testing.T, tl *Tool, _ string) {
		res, err := tl.Run(context.Background(), args("Write-Output '你好，世界'", 0))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(res.Stdout); got != "你好，世界" {
			t.Fatalf("中文输出得到 %q（stderr: %q）", got, res.Stderr)
		}
	})
}

func TestPowerShellNativeProgramChineseOutput(t *testing.T) {
	// 原生程序按控制台代码页输出；这里用 cmd /c 回显中文，验证从原生程序拿到的也是 UTF-8。
	eachEdition(t, func(t *testing.T, tl *Tool, _ string) {
		res, err := tl.Run(context.Background(), args("cmd /c echo 你好，世界", 0))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(res.Stdout); got != "你好，世界" {
			t.Fatalf("原生程序中文输出得到 %q", got)
		}
	})
}

func TestPowerShellStderrAndFailure(t *testing.T) {
	eachEdition(t, func(t *testing.T, tl *Tool, _ string) {
		res, err := tl.Run(context.Background(), args("Get-Item 'Z:\\definitely\\missing'", 0))
		if err != nil {
			t.Fatal(err)
		}
		if res.ExitCode == 0 || res.Stderr == "" {
			t.Fatalf("失败命令应有非零退出码和 stderr，得到 %+v", res)
		}
	})
}

func TestPowerShellNoProgressNoise(t *testing.T) {
	eachEdition(t, func(t *testing.T, tl *Tool, _ string) {
		res, err := tl.Run(context.Background(), args("Write-Progress -Activity x; Write-Output ok", 0))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(res.Stderr, "CLIXML") || strings.TrimSpace(res.Stdout) != "ok" {
			t.Fatalf("不应出现 CLIXML 噪声，得到 %+v", res)
		}
	})
}

func TestPowerShellEditionSyntax(t *testing.T) {
	// 工具描述声称 5.1 不支持 &&，7+ 支持；这里用真实进程验证描述没有说谎。
	eachEdition(t, func(t *testing.T, tl *Tool, ed string) {
		res, err := tl.Run(context.Background(), args("Write-Output a && Write-Output b", 0))
		if err != nil {
			t.Fatal(err)
		}
		works := res.ExitCode == 0 && strings.Contains(res.Stdout, "b")
		if ed == "7+" && !works {
			t.Errorf("7+ 应支持 &&，得到 %+v", res)
		}
		if ed == "5.1" && works {
			t.Errorf("5.1 不应支持 &&，得到 %+v", res)
		}
	})
}

func TestPowerShellTimeoutKillsTree(t *testing.T) {
	eachEdition(t, func(t *testing.T, tl *Tool, _ string) {
		start := time.Now()
		// ping 是 powershell 的子进程，只杀 powershell 本身会让它继续占着管道。
		res, err := tl.Run(context.Background(), args("ping -n 30 127.0.0.1", 1))
		if err != nil {
			t.Fatal(err)
		}
		if !res.TimedOut {
			t.Fatalf("应超时，得到 %+v", res)
		}
		if time.Since(start) > 8*time.Second {
			t.Fatalf("超时后耗时 %v，进程树可能没被终止", time.Since(start))
		}
	})
}

func TestPowerShellStdinIsEmpty(t *testing.T) {
	eachEdition(t, func(t *testing.T, tl *Tool, _ string) {
		res, err := tl.Run(context.Background(), args("[Console]::In.ReadToEnd().Length", 10))
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(res.Stdout) != "0" || res.TimedOut {
			t.Fatalf("读 stdin 应立即得到 EOF，得到 %+v", res)
		}
	})
}

func TestPowerShellTruncatesLargeOutput(t *testing.T) {
	eachEdition(t, func(t *testing.T, tl *Tool, _ string) {
		res, err := tl.Run(context.Background(), args("'x' * 200000", 30))
		if err != nil {
			t.Fatal(err)
		}
		if !res.Truncated || len(res.Stdout) > 50*1024 {
			t.Fatalf("Truncated=%v，stdout %d 字节", res.Truncated, len(res.Stdout))
		}
	})
}

func TestPowerShellDescriptionByEdition(t *testing.T) {
	v7 := New(platform.Shell{Kind: platform.KindPowerShell, Label: "PowerShell 7+", Edition: "7+"}, "")
	v5 := New(platform.Shell{Kind: platform.KindPowerShell, Label: "PowerShell 5.1", Edition: "5.1"}, "")

	if d := v7.Description(); !strings.Contains(d, "PowerShell 7+") || !strings.Contains(d, "支持现代语法") {
		t.Errorf("7+ 描述不对: %s", d)
	}
	d := v5.Description()
	for _, want := range []string{"PowerShell 5.1", "不支持 &&", "?:", "2>&1", "全新的进程"} {
		if !strings.Contains(d, want) {
			t.Errorf("5.1 描述缺少 %q: %s", want, d)
		}
	}
	if v5.Name() != "powershell" {
		t.Errorf("Name = %q", v5.Name())
	}
}

func TestPowerShellInvalidArgs(t *testing.T) {
	for _, sh := range editions(t) {
		if _, err := New(sh, "").Run(context.Background(), json.RawMessage(`not json`)); err == nil {
			t.Fatal("非法参数应返回 error")
		}
		return
	}
}
