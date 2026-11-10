//go:build windows

package platform

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeReg 是用户 PATH 的替身，记录写入次数。只在 _test.go 里，测试不碰真实注册表。
type fakeReg struct {
	path       string
	expand     bool
	readErr    error
	writes     int
	broadcasts int
	system     string
}

// testHost 返回一个所有目录都在临时目录里的 winHost。expand 只认 %ROOT%，展开成临时根目录。
func testHost(t *testing.T, reg *fakeReg) (winHost, string) {
	t.Helper()
	root := t.TempDir()
	exts := exeExts("")
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h := winHost{
		binDir: filepath.Join(root, "sprout", "bin"), backupDir: filepath.Join(root, "sprout"),
		readUser: func() (string, bool, error) { return reg.path, reg.expand, reg.readErr },
		writeUser: func(v string, e bool) error {
			reg.path, reg.expand, reg.writes = v, e, reg.writes+1
			return nil
		},
		readSystem: func() string { return reg.system },
		broadcast:  func() { reg.broadcasts++ },
		expand:     func(s string) string { return strings.ReplaceAll(s, "%ROOT%", root) },
		dirInfo:    func(d string) (bool, bool) { return sproutDirInfo(d, exts) },
		short:      func(s string) string { return s },
		now:        func() time.Time { clock = clock.Add(time.Millisecond); return clock },
	}
	return h, root
}

// mkdir 在 root 下建目录并放进给定的文件，返回目录路径。
func mkdir(t *testing.T, root, name string, files ...string) string {
	t.Helper()
	d := filepath.Join(root, name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "/") {
			os.MkdirAll(filepath.Join(d, f), 0o755)
			continue
		}
		os.WriteFile(filepath.Join(d, f), []byte("x"), 0o644)
	}
	return d
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInstallWritesShimAndPrependsPath(t *testing.T) {
	reg := &fakeReg{path: `C:\A;C:\B`, expand: true}
	h, root := testHost(t, reg)
	target := filepath.Join(mkdir(t, root, "app", "sprout.exe"), "sprout.exe")

	report, err := h.install(target)
	if err != nil {
		t.Fatal(err)
	}
	if want := h.binDir + `;C:\A;C:\B`; reg.path != want || !reg.expand || reg.writes != 1 || reg.broadcasts != 1 {
		t.Fatalf("PATH = %q expand=%v writes=%d broadcasts=%d", reg.path, reg.expand, reg.writes, reg.broadcasts)
	}
	shim := readFile(t, filepath.Join(h.binDir, "sprout.cmd"))
	if shim != shimContent(target) || !strings.Contains(shim, `"`+target+`" %*`) || !isASCII(shim) {
		t.Fatalf("shim = %q", shim)
	}
	if strings.Count(shim, "\r\n") != 4 || strings.Count(shim, "\n") != 4 {
		t.Errorf(".cmd 必须是 CRLF: %q", shim)
	}
	backups, _ := filepath.Glob(filepath.Join(h.backupDir, "path-backup-*.txt"))
	if len(backups) != 1 || readFile(t, backups[0]) != `C:\A;C:\B` {
		t.Fatalf("改 PATH 前应备份原值: %v", backups)
	}
	for _, want := range []string{"已注册", target, "放到用户 PATH 最前面", "已备份到", "新开的终端"} {
		if !strings.Contains(report, want) {
			t.Errorf("报告缺少 %q:\n%s", want, report)
		}
	}
}

func TestInstallTwiceChangesNothingTheSecondTime(t *testing.T) {
	reg := &fakeReg{path: `C:\A`}
	h, root := testHost(t, reg)
	target := filepath.Join(mkdir(t, root, "app", "sprout.exe"), "sprout.exe")
	h.install(target)
	report, err := h.install(target)
	if err != nil {
		t.Fatal(err)
	}
	if reg.writes != 1 || !strings.Contains(report, "没有改动") || strings.Contains(report, "已备份") {
		t.Fatalf("第二次不应再改 PATH: writes=%d\n%s", reg.writes, report)
	}
}

func TestInstallMovesExistingEntryToFront(t *testing.T) {
	reg := &fakeReg{}
	h, root := testHost(t, reg)
	reg.path = `C:\A;` + strings.ToUpper(h.binDir) + `\;C:\B` // 大小写、末尾反斜杠不同也算同一个
	target := filepath.Join(mkdir(t, root, "app", "sprout.exe"), "sprout.exe")
	h.install(target)
	if want := h.binDir + `;C:\A;C:\B`; reg.path != want {
		t.Fatalf("PATH = %q，期望 %q（去重并移到最前）", reg.path, want)
	}
}

func TestInstallRemovesDirsHoldingOnlyOldSprout(t *testing.T) {
	reg := &fakeReg{}
	h, root := testHost(t, reg)
	old := mkdir(t, root, "old", "sprout.exe", "sprout-data/", "readme.txt")
	shared := mkdir(t, root, "shared", "sprout.exe", "claude.cmd")
	app := mkdir(t, root, "app", "sprout.exe", "sprout_birth.exe") // 被注册的程序所在目录
	reg.path = strings.Join([]string{shared, old, `C:\X`, app}, ";")

	report, err := h.install(filepath.Join(app, "sprout.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Join([]string{h.binDir, shared, `C:\X`, app}, ";"); reg.path != want {
		t.Fatalf("PATH = %q\n期望 %q", reg.path, want)
	}
	if !strings.Contains(report, "已从用户 PATH 移除只装着旧 sprout 的目录："+old) {
		t.Errorf("应报告移除了 %s:\n%s", old, report)
	}
	if !strings.Contains(report, shared+" 里也有 sprout，但还有别的程序") {
		t.Errorf("应提示保留了 %s:\n%s", shared, report)
	}
	if _, err := os.Stat(filepath.Join(old, "sprout.exe")); err != nil {
		t.Error("只改 PATH，不能删旧目录里的文件")
	}
	if strings.Contains(report, app+" 里也有") {
		t.Errorf("被注册的程序所在目录不算冲突:\n%s", report)
	}
}

func TestInstallKeepsEntriesVerbatim(t *testing.T) {
	// 未展开的 %VAR% 条目原样保留；用 %VAR% 写的、指向安装目录的条目也能认出来去重。
	reg := &fakeReg{path: `%ROOT%\sprout\bin;%USERPROFILE%\bin;C:\X`, expand: true}
	h, root := testHost(t, reg)
	h.install(filepath.Join(mkdir(t, root, "app", "sprout.exe"), "sprout.exe"))
	if want := h.binDir + `;%USERPROFILE%\bin;C:\X`; reg.path != want || !reg.expand {
		t.Fatalf("PATH = %q expand=%v\n期望 %q", reg.path, reg.expand, want)
	}
}

func TestInstallRefusesForeignShim(t *testing.T) {
	reg := &fakeReg{path: `C:\A`}
	h, root := testHost(t, reg)
	os.MkdirAll(h.binDir, 0o755)
	shim := filepath.Join(h.binDir, "sprout.cmd")
	os.WriteFile(shim, []byte("@echo something else"), 0o644)

	if _, err := h.install(filepath.Join(mkdir(t, root, "app", "sprout.exe"), "sprout.exe")); err == nil ||
		!strings.Contains(err.Error(), "不是 /install-sprout 生成的") {
		t.Fatalf("err = %v", err)
	}
	if readFile(t, shim) != "@echo something else" || reg.writes != 0 {
		t.Fatal("别人的文件不能覆盖，PATH 也不能改")
	}
}

func TestInstallReadErrorWritesNothing(t *testing.T) {
	reg := &fakeReg{readErr: errors.New("拒绝访问")}
	h, root := testHost(t, reg)
	if _, err := h.install(filepath.Join(mkdir(t, root, "app", "sprout.exe"), "sprout.exe")); err == nil {
		t.Fatal("读不到 PATH 应报错")
	}
	if _, err := os.Stat(filepath.Join(h.binDir, "sprout.cmd")); err == nil {
		t.Fatal("出错时不能留下半截的安装")
	}
}

func TestInstallNonASCIIPathUsesShortName(t *testing.T) {
	reg := &fakeReg{}
	h, _ := testHost(t, reg)
	h.short = func(string) string { return `C:\PROGRA~1\sprout.exe` }
	if _, err := h.install(`C:\程序\sprout.exe`); err != nil {
		t.Fatal(err)
	}
	if shim := readFile(t, filepath.Join(h.binDir, "sprout.cmd")); !strings.Contains(shim, `"C:\PROGRA~1\sprout.exe" %*`) {
		t.Fatalf("应改用 8.3 短路径: %q", shim)
	}

	reg2 := &fakeReg{}
	h2, _ := testHost(t, reg2)
	h2.short = func(s string) string { return s } // 卷上关闭了短文件名
	if _, err := h2.install(`C:\程序\sprout.exe`); err == nil || !strings.Contains(err.Error(), "非 ASCII") {
		t.Fatalf("err = %v", err)
	}
	if reg2.writes != 0 {
		t.Fatal("拒绝时不能改 PATH")
	}
}

func TestShimEscapesPercent(t *testing.T) {
	if s := shimContent(`C:\100%\sprout.exe`); !strings.Contains(s, `"C:\100%%\sprout.exe" %*`) {
		t.Fatalf("%% 要转义成 %%%%: %q", s)
	}
}

func TestInstallWarnsAboutSproutInSystemPath(t *testing.T) {
	reg := &fakeReg{}
	h, root := testHost(t, reg)
	sys := mkdir(t, root, "sys", "sprout.exe")
	reg.system = `C:\Windows;` + sys
	report, _ := h.install(filepath.Join(mkdir(t, root, "app", "sprout.exe"), "sprout.exe"))
	if !strings.Contains(report, "系统 PATH 里的 "+sys) || !strings.Contains(report, "管理员权限") {
		t.Fatalf("应提示系统 PATH 里的冲突:\n%s", report)
	}
}

func TestUninstallUndoesInstall(t *testing.T) {
	reg := &fakeReg{path: `C:\A;C:\B`, expand: true}
	h, root := testHost(t, reg)
	h.install(filepath.Join(mkdir(t, root, "app", "sprout.exe"), "sprout.exe"))

	report, err := h.uninstall()
	if err != nil {
		t.Fatal(err)
	}
	if reg.path != `C:\A;C:\B` || !reg.expand || reg.broadcasts != 2 {
		t.Fatalf("PATH = %q expand=%v broadcasts=%d", reg.path, reg.expand, reg.broadcasts)
	}
	if _, err := os.Stat(h.binDir); err == nil {
		t.Error("空的安装目录应删掉")
	}
	if backups, _ := filepath.Glob(filepath.Join(h.backupDir, "path-backup-*.txt")); len(backups) != 2 {
		t.Errorf("安装和卸载各备份一次，得到 %d 个", len(backups))
	}
	if !strings.Contains(report, "已卸载") || !strings.Contains(report, "程序文件本身没有动") {
		t.Errorf("报告:\n%s", report)
	}
}

func TestUninstallWhenNothingInstalled(t *testing.T) {
	reg := &fakeReg{path: `C:\A`}
	h, _ := testHost(t, reg)
	report, err := h.uninstall()
	if err != nil || reg.writes != 0 || !strings.Contains(report, "无需卸载") {
		t.Fatalf("report=%q err=%v writes=%d", report, err, reg.writes)
	}
}

func TestUninstallRefusesForeignShim(t *testing.T) {
	reg := &fakeReg{}
	h, _ := testHost(t, reg)
	reg.path = h.binDir
	os.MkdirAll(h.binDir, 0o755)
	os.WriteFile(filepath.Join(h.binDir, "sprout.cmd"), []byte("别人的"), 0o644)
	if _, err := h.uninstall(); err == nil || reg.writes != 0 {
		t.Fatalf("不是我们的文件不能删: err=%v writes=%d", err, reg.writes)
	}
}

func TestExpandWin(t *testing.T) {
	env := map[string]string{"A": `C:\a`, "B": "b"}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	for in, want := range map[string]string{
		`%A%\bin`:  `C:\a\bin`,
		`%A%%B%`:   `C:\ab`,
		`%NOPE%\x`: `%NOPE%\x`,
		`100%`:     `100%`,
		`%%`:       `%%`,
		`x%A%y%`:   `xC:\ay%`,
	} {
		if got := expandWin(in, lookup); got != want {
			t.Errorf("expandWin(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSameWinPath(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{`C:\X\Bin`, `c:\x\bin`, true},
		{`C:\X\Bin\`, `C:\X\Bin`, true},
		{` "C:\X" `, `C:\X`, true},
		{`C:\X`, `C:\Y`, false},
		{``, ``, false},
	} {
		if got := sameWinPath(c.a, c.b); got != c.want {
			t.Errorf("sameWinPath(%q, %q) = %v", c.a, c.b, got)
		}
	}
}
