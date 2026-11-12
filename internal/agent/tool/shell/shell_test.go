package shell

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// helperEnv 非空时，测试二进制扮演被执行的子进程，而不是跑测试。
// 这样 Run 的测试不依赖任何外部 shell，在所有平台上行为一致。
const helperEnv = "SPROUT_SHELL_TEST_HELPER"

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		helperMain(mode)
		return
	}
	os.Exit(m.Run())
}

func helperMain(mode string) {
	switch mode {
	case "out":
		fmt.Fprint(os.Stdout, "hello")
		fmt.Fprint(os.Stderr, "oops")
	case "exit3":
		os.Exit(3)
	case "sleep":
		time.Sleep(time.Minute)
	case "flood":
		chunk := []byte(strings.Repeat("x", 1024))
		for i := 0; i < 4096; i++ { // 4MB
			os.Stdout.Write(chunk)
		}
	case "chinese":
		fmt.Fprint(os.Stdout, "你好，世界")
	case "stdin":
		n, _ := io.Copy(io.Discard, os.Stdin)
		fmt.Fprintf(os.Stdout, "stdin=%d", n)
	case "tree":
		// 起一个继承输出管道的长寿孙进程，自己也一直等着。
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), helperEnv+"=sleep20")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		child.Start()
		time.Sleep(time.Minute)
	case "orphan":
		// 起一个继承输出管道的后台孙进程，然后立刻正常退出。
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), helperEnv+"=sleep3")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		child.Start()
		fmt.Fprint(os.Stdout, "done")
	case "consolepids":
		printConsolePIDs()
	case "sleep3":
		time.Sleep(3 * time.Second)
	case "sleep20":
		time.Sleep(20 * time.Second)
	}
	os.Exit(0)
}

func helperOpts(t *testing.T, mode string, timeout time.Duration) Options {
	t.Helper()
	t.Setenv(helperEnv, mode)
	// 不指定 Dir：孙进程会继承工作目录，Windows 上占用 t.TempDir() 会导致清理失败。
	return Options{Path: os.Args[0], Timeout: timeout}
}

func TestRunCapturesStdoutStderr(t *testing.T) {
	out, err := Run(context.Background(), helperOpts(t, "out", 0))
	if err != nil {
		t.Fatal(err)
	}
	if out.Stdout != "hello" || out.Stderr != "oops" || out.ExitCode != 0 {
		t.Fatalf("得到 %+v", out)
	}
	if out.Duration <= 0 {
		t.Error("Duration 应大于 0")
	}
}

func TestRunNonZeroExitIsNotError(t *testing.T) {
	out, err := Run(context.Background(), helperOpts(t, "exit3", 0))
	if err != nil {
		t.Fatalf("非零退出码不应是 error: %v", err)
	}
	if out.ExitCode != 3 || out.TimedOut {
		t.Fatalf("得到 %+v", out)
	}
}

func TestRunChineseOutput(t *testing.T) {
	out, err := Run(context.Background(), helperOpts(t, "chinese", 0))
	if err != nil || out.Stdout != "你好，世界" {
		t.Fatalf("得到 %q, %v", out.Stdout, err)
	}
}

func TestRunStdinIsEmpty(t *testing.T) {
	opts := helperOpts(t, "stdin", 10*time.Second)
	done := make(chan Output, 1)
	go func() {
		out, _ := Run(context.Background(), opts)
		done <- out
	}()
	select {
	case out := <-done:
		if out.Stdout != "stdin=0" {
			t.Fatalf("子进程应读到空 stdin，得到 %q", out.Stdout)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("子进程读 stdin 被挂住，应立即得到 EOF")
	}
}

func TestRunTimeout(t *testing.T) {
	out, err := Run(context.Background(), helperOpts(t, "sleep", 300*time.Millisecond))
	if err != nil {
		t.Fatalf("超时不应是 error: %v", err)
	}
	if !out.TimedOut || out.ExitCode != -1 {
		t.Fatalf("得到 %+v", out)
	}
}

func TestRunTimeoutKillsProcessTree(t *testing.T) {
	out, err := Run(context.Background(), helperOpts(t, "tree", 300*time.Millisecond))
	if err != nil || !out.TimedOut {
		t.Fatalf("得到 %+v, %v", out, err)
	}
	// 进程树被整体终止，管道随之关闭；否则要白等满 waitDelay（2s）。
	if out.Duration > 1500*time.Millisecond {
		t.Fatalf("耗时 %v，孙进程可能没被终止", out.Duration)
	}
}

func TestRunCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	out, err := Run(ctx, helperOpts(t, "sleep", time.Minute))
	if err != context.Canceled {
		t.Fatalf("取消应返回 context.Canceled，得到 %v", err)
	}
	if out.TimedOut {
		t.Error("取消不应被标成超时")
	}
}

func TestRunTruncatesFlood(t *testing.T) {
	out, err := Run(context.Background(), helperOpts(t, "flood", 30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !out.Truncated {
		t.Fatal("4MB 输出应被标记截断")
	}
	if len(out.Stdout) > MaxOutput {
		t.Fatalf("stdout %d 字节，超过 %d 上限", len(out.Stdout), MaxOutput)
	}
	if !strings.Contains(out.Stdout, "中间已截断") {
		t.Error("应包含截断标记")
	}
}

func TestRunOrphanKeepsPipeOpen(t *testing.T) {
	// 进程正常退出但后台孙进程握着管道：结果仍应可用，且不会等到孙进程结束。
	out, err := Run(context.Background(), helperOpts(t, "orphan", time.Minute))
	if err != nil {
		t.Fatalf("得到 error: %v", err)
	}
	if out.Stdout != "done" || out.ExitCode != 0 || out.TimedOut {
		t.Fatalf("得到 %+v", out)
	}
	if out.Duration > 5*time.Second {
		t.Fatalf("耗时 %v，不应等孙进程结束", out.Duration)
	}
	// 孙进程还要活约 1 秒。等它退出，否则它仍占着测试 exe，Windows 无法删除，
	// go test 会在收尾时报 "unlinkat ... Access is denied"。
	time.Sleep(1500 * time.Millisecond)
}

func TestRunStartFailure(t *testing.T) {
	_, err := Run(context.Background(), Options{Path: "Z:\\definitely\\not\\here\\nope", Timeout: time.Second})
	if err == nil || !strings.Contains(err.Error(), "无法启动") {
		t.Fatalf("得到 %v", err)
	}
}
