package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCleanOlderDeletesOnlyOldSessions(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	old := mkSession(t, root, "old11111", now.Add(-40*24*time.Hour), "很久以前")
	empty := mkSession(t, root, "empty000", now.Add(-35*24*time.Hour)) // 没有提问，列表里看不到，也要清
	keep := mkSession(t, root, "new22222", now.Add(-time.Hour), "最近的")
	active := mkSession(t, root, "act33333", now.Add(-50*24*time.Hour), "正在用的")
	base := strings.TrimSuffix(old, ".jsonl")
	for _, d := range []string{base + ".images", base + ".agents"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cutoff := now.Add(-30 * 24 * time.Hour)
	// dry-run 只列出，不动文件。
	got, err := CleanOlder(root, cutoff, active, true)
	if err != nil || len(got) != 2 || got[0].ID != "old11111" || got[1].Path != empty {
		t.Fatalf("dry-run 应从旧到新列出两个: %+v %v", got, err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("dry-run 不应删除文件")
	}

	got, err = CleanOlder(root, cutoff, active, false)
	if err != nil || len(got) != 2 {
		t.Fatalf("应删掉两个: %+v %v", got, err)
	}
	for _, p := range []string{old, empty, base + ".images", base + ".agents"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s 应已删除", filepath.Base(p))
		}
	}
	for _, p := range []string{keep, active} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s 不应被删: %v", filepath.Base(p), err)
		}
	}
}

func TestCleanOlderRemovesEmptiedGroupDir(t *testing.T) {
	root := t.TempDir()
	p := mkSession(t, root, "old11111", time.Now().Add(-40*24*time.Hour), "旧的")
	if _, err := CleanOlder(root, time.Now().Add(-30*24*time.Hour), "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(p)); !os.IsNotExist(err) {
		t.Error("删空的分组目录应移除")
	}
}
