package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func exeAt(p string) func() (string, error) {
	return func() (string, error) { return p, nil }
}

func TestDataDirPrecedence(t *testing.T) {
	root := t.TempDir()
	flagDir := filepath.Join(root, "flag")
	envDir := filepath.Join(root, "env")
	exe := filepath.Join(root, "bin", "sprout.exe")

	got, err := resolveDataDir(flagDir, envDir, exeAt(exe))
	if err != nil || got != flagDir {
		t.Fatalf("flag 应最优先，得到 %q, %v", got, err)
	}
	got, err = resolveDataDir("", envDir, exeAt(exe))
	if err != nil || got != envDir {
		t.Fatalf("env 次之，得到 %q, %v", got, err)
	}
	got, err = resolveDataDir("", "", exeAt(exe))
	want := filepath.Join(root, "bin", "data", ".sprout")
	if err != nil || got != want {
		t.Fatalf("默认应在 exe 同级 data/.sprout，得到 %q, %v，期望 %q", got, err, want)
	}
	if st, err := os.Stat(want); err != nil || !st.IsDir() {
		t.Fatal("解析后目录应已创建")
	}
}

func TestDataDirRejectsGoRunTempBinary(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "go-build123456", "b001", "exe", "sprout.exe")
	_, err := resolveDataDir("", "", exeAt(exe))
	if err == nil || !strings.Contains(err.Error(), "--data-dir") || !strings.Contains(err.Error(), "go run") {
		t.Fatalf("go run 启动且未指定目录时应报错并提示 --data-dir，得到 %v", err)
	}
	// 显式指定则不受影响。
	if _, err := resolveDataDir(filepath.Join(t.TempDir(), "d"), "", exeAt(exe)); err != nil {
		t.Fatalf("显式指定后应放行: %v", err)
	}
}

func TestDataDirNotWritableReportsHint(t *testing.T) {
	// 路径上有一个普通文件，MkdirAll 必然失败，且不依赖权限模型，在 Windows 上同样可靠。
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := resolveDataDir(filepath.Join(blocker, "sub"), "", exeAt("unused"))
	if err == nil || !strings.Contains(err.Error(), "--data-dir") {
		t.Fatalf("不可写时应报错并提示 --data-dir，得到 %v", err)
	}
}

func TestDataDirDoesNotLeaveProbeFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	if _, err := resolveDataDir(dir, "", exeAt("unused")); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("可写性探测不应留下文件: %v", entries)
	}
}

func TestDataDirRelativeBecomesAbsolute(t *testing.T) {
	t.Chdir(t.TempDir())
	got, err := resolveDataDir(filepath.Join(".", "rel", ".sprout"), "", exeAt("unused"))
	if err != nil || !filepath.IsAbs(got) {
		t.Fatalf("相对路径应转成绝对路径，得到 %q, %v", got, err)
	}
}

func TestDataDirExecutableError(t *testing.T) {
	_, err := resolveDataDir("", "", func() (string, error) { return "", errors.New("boom") })
	if err == nil || !strings.Contains(err.Error(), "--data-dir") {
		t.Fatalf("得到 %v", err)
	}
}

func TestIsGoBuildPath(t *testing.T) {
	for p, want := range map[string]bool{
		`C:\Users\a\AppData\Local\Temp\go-build123\b001\exe\x.exe`: true,
		`/tmp/go-build99/b001/exe/x`:                               true,
		`C:\apps\sprout\bin\sprout.exe`:                            false,
		`/usr/local/bin/sprout`:                                    false,
	} {
		if got := isGoBuildPath(p); got != want {
			t.Errorf("isGoBuildPath(%q) = %v", p, got)
		}
	}
}

func TestSubdirHelpers(t *testing.T) {
	d := filepath.Join("x", ".sprout")
	if ModelsPath(d) != filepath.Join(d, "models.jsonc") ||
		SessionsDir(d) != filepath.Join(d, "sessions") ||
		LogsDir(d) != filepath.Join(d, "logs") {
		t.Fatal("子路径不对")
	}
}
