package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeLinkFS 是文件系统替身：links 是符号链接（路径 → 指向），files 是普通文件，exec 是可执行文件。
type fakeLinkFS struct {
	links map[string]string
	files map[string]bool
	exec  map[string]bool
	dirs  []string
}

func newFakeLinkFS() *fakeLinkFS {
	return &fakeLinkFS{links: map[string]string{}, files: map[string]bool{}, exec: map[string]bool{}}
}

func (f *fakeLinkFS) state(p string) (pathState, string, error) {
	if t, ok := f.links[p]; ok {
		return pathSymlink, t, nil
	}
	if f.files[p] {
		return pathRegular, "", nil
	}
	return pathMissing, "", nil
}
func (f *fakeLinkFS) symlink(target, link string) error { f.links[link] = target; return nil }
func (f *fakeLinkFS) remove(p string) error             { delete(f.links, p); return nil }
func (f *fakeLinkFS) mkdirAll(dir string) error         { f.dirs = append(f.dirs, dir); return nil }
func (f *fakeLinkFS) isExec(p string) bool              { return f.exec[p] }

var (
	tBin    = filepath.Join("home", "u", ".local", "bin")
	tTarget = filepath.Join("opt", "sprout", "sprout")
	tLink   = filepath.Join(tBin, "sprout")
)

func pathOf(dirs ...string) string { return strings.Join(dirs, string(filepath.ListSeparator)) }

func TestLinkInstallCreatesSymlink(t *testing.T) {
	fs := newFakeLinkFS()
	h := linkHost{fs: fs, binDir: tBin, path: pathOf(tBin, "usr")}
	report, err := h.install(tTarget)
	if err != nil {
		t.Fatal(err)
	}
	if fs.links[tLink] != tTarget || len(fs.dirs) != 1 {
		t.Fatalf("links=%v dirs=%v", fs.links, fs.dirs)
	}
	if !strings.Contains(report, "已注册") || strings.Contains(report, "不在 PATH") {
		t.Errorf("报告:\n%s", report)
	}
}

func TestLinkInstallIsIdempotent(t *testing.T) {
	fs := newFakeLinkFS()
	fs.links[tLink] = tTarget
	report, _ := linkHost{fs: fs, binDir: tBin, path: tBin}.install(tTarget)
	if len(fs.dirs) != 0 || !strings.Contains(report, "已经指向当前程序") {
		t.Fatalf("已经是对的就不应再改:\n%s", report)
	}
}

func TestLinkInstallReplacesOldSymlink(t *testing.T) {
	fs := newFakeLinkFS()
	old := filepath.Join("old", "sprout")
	fs.links[tLink] = old
	report, err := linkHost{fs: fs, binDir: tBin, path: tBin}.install(tTarget)
	if err != nil || fs.links[tLink] != tTarget || !strings.Contains(report, "原来的 sprout 命令指向 "+old) {
		t.Fatalf("err=%v links=%v\n%s", err, fs.links, report)
	}
}

func TestLinkInstallRefusesRegularFile(t *testing.T) {
	fs := newFakeLinkFS()
	fs.files[tLink] = true
	if _, err := (linkHost{fs: fs, binDir: tBin, path: tBin}).install(tTarget); err == nil || len(fs.links) != 0 {
		t.Fatalf("普通文件不能覆盖: err=%v", err)
	}
}

func TestLinkInstallExplainsMissingPath(t *testing.T) {
	report, _ := linkHost{fs: newFakeLinkFS(), binDir: tBin, path: "usr"}.install(tTarget)
	if !strings.Contains(report, "不在 PATH 里") || !strings.Contains(report, `export PATH="`+tBin+`:$PATH"`) {
		t.Fatalf("应提示怎么把目录加进 PATH:\n%s", report)
	}
}

func TestLinkInstallWarnsAboutEarlierSprout(t *testing.T) {
	fs := newFakeLinkFS()
	early, late := filepath.Join("early"), filepath.Join("late")
	fs.exec[filepath.Join(early, "sprout")] = true
	fs.exec[filepath.Join(late, "sprout")] = true
	report, _ := linkHost{fs: fs, binDir: tBin, path: pathOf(early, tBin, late)}.install(tTarget)
	if !strings.Contains(report, filepath.Join(early, "sprout")) {
		t.Errorf("应提示更靠前的 sprout:\n%s", report)
	}
	if strings.Contains(report, filepath.Join(late, "sprout")) {
		t.Errorf("更靠后的不会被用到，不用提示:\n%s", report)
	}
}

func TestLinkUninstall(t *testing.T) {
	fs := newFakeLinkFS()
	fs.links[tLink] = tTarget
	h := linkHost{fs: fs, binDir: tBin}
	if report, err := h.uninstall(); err != nil || len(fs.links) != 0 || !strings.Contains(report, "已卸载") {
		t.Fatalf("err=%v links=%v\n%s", err, fs.links, report)
	}
	if report, err := h.uninstall(); err != nil || !strings.Contains(report, "无需卸载") {
		t.Fatalf("没装时: %q %v", report, err)
	}
	fs.files[tLink] = true
	if _, err := h.uninstall(); err == nil {
		t.Fatal("普通文件不是我们建的，不能删")
	}
}

func TestInstallTarget(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "sprout.exe")
	os.WriteFile(exe, []byte("x"), 0o755)
	if got, err := installTarget(exe); err != nil || !strings.EqualFold(filepath.Base(got), "sprout.exe") || !filepath.IsAbs(got) {
		t.Fatalf("got %q err %v", got, err)
	}

	tmp := filepath.Join(dir, "go-build123", "b001", "exe")
	os.MkdirAll(tmp, 0o755)
	os.WriteFile(filepath.Join(tmp, "sprout.exe"), []byte("x"), 0o755)
	if _, err := installTarget(filepath.Join(tmp, "sprout.exe")); err == nil || !strings.Contains(err.Error(), "go run") {
		t.Fatalf("go run 的临时程序不能注册: %v", err)
	}
	if _, err := installTarget(filepath.Join(dir, "missing.exe")); err == nil {
		t.Fatal("不存在的程序应报错")
	}
	if _, err := installTarget(dir); err == nil {
		t.Fatal("目录应报错")
	}
}
