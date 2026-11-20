package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shumengya/sprout/internal/agent/config"
)

func TestCleanOldLogs(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.Local)
	files := map[string]bool{ // 文件名 → 清理后是否还在
		"sprout-20261001.log": false, // 50 天前
		"sprout-20261020.log": false, // 那一整天都在 30 天以前
		"sprout-20261022.log": true,  // 29 天前
		"sprout-20261119.log": true,
		"sprout-坏日期.log":      true, // 不是这个命名的不碰
		"notes.txt":           true,
	}
	for name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cleanOldLogs(dir, now)
	for name, keep := range files {
		_, err := os.Stat(filepath.Join(dir, name))
		if keep && err != nil {
			t.Errorf("%s 不应被删", name)
		}
		if !keep && !os.IsNotExist(err) {
			t.Errorf("%s 应已删除", name)
		}
	}
}

func TestParseSessionsClean(t *testing.T) {
	a, err := parseArgs([]string{"sessions", "clean", "--older-than", "30d", "--dry-run", "--data-dir", "D:/x"})
	if err != nil || !a.sessionsClean || a.cleanAge != 30*24*time.Hour || !a.cleanDry || a.dataDir != "D:/x" {
		t.Fatalf("得到 %+v %v", a, err)
	}
	for in, want := range map[string]time.Duration{"7": 7 * 24 * time.Hour, "12h": 12 * time.Hour, "90m": 90 * time.Minute} {
		if got, err := parseAge(in); err != nil || got != want {
			t.Errorf("parseAge(%q) = %v %v", in, got, err)
		}
	}
	for _, argv := range [][]string{
		{"sessions"}, {"sessions", "list"}, {"sessions", "clean"}, // 删除不给默认时长
		{"sessions", "clean", "--older-than", "0d"}, {"sessions", "clean", "--older-than", "abc"},
		{"sessions", "clean", "--older-than", "30d", "多余"},
	} {
		if _, err := parseArgs(argv); !errors.Is(err, errUsage) {
			t.Errorf("%v 应是用法错误，得到 %v", argv, err)
		}
	}
}

// writeOldSession 在数据目录里造一个会话文件，最后修改时间是 age 之前。
func writeOldSession(t *testing.T, dataDir, id string, age time.Duration) string {
	t.Helper()
	dir := filepath.Join(config.SessionsDir(dataDir), "--D--proj--")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "20260101-000000_"+id+".jsonl")
	body := `{"type":"session","id":"` + id + `","cwd":"D:\\proj"}` + "\n" + `{"type":"user","content":"旧提问 ` + id + `"}` + "\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	mod := time.Now().Add(-age)
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSessionsCleanCommand(t *testing.T) {
	dir := t.TempDir()
	old := writeOldSession(t, dir, "old00001", 40*24*time.Hour)
	recent := writeOldSession(t, dir, "new00002", time.Hour)

	code, out, errOut := run("--data-dir", dir, "sessions", "clean", "--older-than", "30d", "--dry-run")
	if code != 0 || !strings.Contains(out, "旧提问 old00001") || strings.Contains(out, "new00002") || !strings.Contains(out, "没有删除") {
		t.Fatalf("dry-run: code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("dry-run 不应删除")
	}

	code, out, errOut = run("--data-dir", dir, "sessions", "clean", "--older-than", "30d")
	if code != 0 || !strings.Contains(out, "已删除 1 个会话") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("旧会话应已删除")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Error("最近的会话不应被删")
	}
}

// settings.jsonc 里打开 cleanupPeriodDays 后，启动时删掉超期的会话并提示；默认不删。
func TestCleanupPeriodDaysOnStartup(t *testing.T) {
	srv := newModelServerStatus(t, 401, `{"error":{"message":"bad key"}}`) // 只需要走到装配完成，这一轮失败无所谓
	dir := t.TempDir()
	writeServerConfig(t, dir, srv.URL)
	old := writeOldSession(t, dir, "old00001", 40*24*time.Hour)

	run("--data-dir", dir, "run", "q") // 默认 0：不删
	if _, err := os.Stat(old); err != nil {
		t.Fatal("默认不应自动删除会话")
	}

	tmpl := strings.Replace(config.SettingsTemplate, `"cleanupPeriodDays": 0`, `"cleanupPeriodDays": 30`, 1)
	if tmpl == config.SettingsTemplate {
		t.Fatal("模板里没有 cleanupPeriodDays，请同步更新本测试")
	}
	if err := os.WriteFile(config.SettingsPath(dir), []byte(tmpl), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, errOut := run("--data-dir", dir, "run", "q")
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("超过 30 天的会话应在启动时删除")
	}
	if !strings.Contains(errOut, "已删除 1 个超过 30 天没用过的会话") {
		t.Errorf("应提示删了几个: %q", errOut)
	}
}
