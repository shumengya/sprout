package platform

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestSamePath(t *testing.T) {
	dir := t.TempDir()
	if !SamePath(dir, filepath.Join(dir, "x", "..")) {
		t.Error("规整后相同的路径应判为相同")
	}
	if SamePath(dir, filepath.Join(dir, "x")) {
		t.Error("不同路径不应相同")
	}
	upper := filepath.Join(dir, "ABC")
	lower := filepath.Join(dir, "abc")
	want := runtime.GOOS == "windows" || runtime.GOOS == "darwin"
	if SamePath(upper, lower) != want {
		t.Errorf("大小写不同时 SamePath = %v，期望 %v", !want, want)
	}
}
