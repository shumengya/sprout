package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent/config"
)

func TestVersionAndHelp(t *testing.T) {
	if code, out, _ := run("--version"); code != 0 || !strings.HasPrefix(out, "sprout ") {
		t.Errorf("--version: code=%d out=%q", code, out)
	}
	if code, out, _ := run("--help"); code != 0 || !strings.Contains(out, "sprout run") {
		t.Errorf("--help: code=%d out=%q", code, out)
	}
}

func TestUsageErrorsExitWith2(t *testing.T) {
	for _, argv := range [][]string{{"serve"}, {"--nope"}, {"run"}} {
		code, _, errOut := run(argv...)
		if code != 2 || !strings.Contains(errOut, "用法") {
			t.Errorf("%v: code=%d stderr=%q", argv, code, errOut)
		}
	}
}

func TestRunWithoutKeyFailsWithGuidanceAndCreatesTemplate(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	dir := t.TempDir()
	code, out, errOut := run("--data-dir", dir, "run", "你好")
	if code != 1 || out != "" {
		t.Fatalf("code=%d stdout=%q", code, out)
	}
	for _, want := range []string{"错误:", config.ModelsPath(dir), "DEEPSEEK_API_KEY", "重启"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr 缺少 %q: %q", want, errOut)
		}
	}
	if _, err := os.Stat(config.ModelsPath(dir)); err != nil {
		t.Errorf("首次运行应生成 models.jsonc: %v", err)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "sessions", "*", "*")); len(files) != 0 {
		t.Errorf("没有发起对话就不应生成会话文件: %v", files)
	}
}

func TestBadConfigReportsLocationAndExits1(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(config.ModelsPath(dir), []byte("{\n  \"default\": \"a/b\"\n  \"x\": 1\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := run("--data-dir", dir, "run", "q")
	if code != 1 || !strings.Contains(errOut, "models.jsonc:3:") {
		t.Fatalf("应报告 文件:行:列，code=%d stderr=%q", code, errOut)
	}
}

func TestUnwritableDataDirExits1WithHint(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := run("--data-dir", filepath.Join(blocker, "sub"), "run", "q")
	if code != 1 || !strings.Contains(errOut, "--data-dir") {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
}

func TestAuthFailureIsReadableAndExits1(t *testing.T) {
	srv := newModelServerStatus(t, 401, `{"error":{"message":"Incorrect API key","code":"invalid_api_key"}}`)
	dir := t.TempDir()
	writeServerConfig(t, dir, srv.URL)
	code, out, errOut := run("--data-dir", dir, "run", "你好")
	if code != 1 || out != "" {
		t.Fatalf("code=%d stdout=%q", code, out)
	}
	if !strings.Contains(errOut, "认证失败") || !strings.Contains(errOut, "API Key") || strings.Contains(errOut, "sk-e2e") {
		t.Errorf("应给出中文的认证失败提示，且不泄漏 Key: %q", errOut)
	}
	// 对话失败不能 panic，也不应留下半截会话。
	if files, _ := filepath.Glob(filepath.Join(dir, "sessions", "*", "*.jsonl")); len(files) != 1 {
		t.Errorf("失败的一轮也应被记录: %v", files)
	}
}

func TestNetworkFailureIsReadableAndExits1(t *testing.T) {
	srv := newModelServerStatus(t, 200, "")
	srv.Close() // 之后连接必然失败
	dir := t.TempDir()
	writeServerConfig(t, dir, srv.URL)
	code, _, errOut := run("--data-dir", dir, "run", "你好")
	if code != 1 || !strings.Contains(errOut, "通信失败") {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
}
