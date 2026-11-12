package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidVersion(t *testing.T) {
	for _, v := range []string{"2026.11.12", "2026.11.12-rc.1"} {
		if !validVersion(v) {
			t.Errorf("%q 应合法", v)
		}
	}
	// 带 v 前缀、月份越界、日期不存在、后缀为空都不该通过：
	// 版本号一旦写错并发布，就无法收回。
	for _, v := range []string{"", "v2026.11.12", "2026.11", "2026.13.01", "2026.02.30", "2026.11.12-", "dev"} {
		if validVersion(v) {
			t.Errorf("%q 应不合法", v)
		}
	}
}

// 每个平台一个包，名字不重复，且 Windows 用 zip、其余用 tar.gz。
func TestArtifactNames(t *testing.T) {
	seen := map[string]bool{}
	for _, tg := range targets() {
		name := artifactName("2026.11.12", tg)
		if seen[name] {
			t.Fatalf("压缩包重名: %s", name)
		}
		seen[name] = true
		wantZip := tg.GOOS == "windows"
		if strings.HasSuffix(name, ".zip") != wantZip {
			t.Errorf("%s 的扩展名不对: %s", tg.Platform, name)
		}
		if !strings.Contains(name, tg.Platform) {
			t.Errorf("%s 没体现在文件名里: %s", tg.Platform, name)
		}
	}
}

// 压缩包内是 <归档目录>/sprout[.exe] 与 README.md，Unix 二进制带可执行权限。
func TestWriteArchive(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "src-sprout")
	if err := os.WriteFile(bin, []byte("binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	// packageEntries 从仓库根取 README.md 与 LICENSE，这里用临时目录充当仓库根。
	root := filepath.Join(dir, "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# sprout"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "LICENSE"), []byte("Apache License"), 0o644); err != nil {
		t.Fatal(err)
	}
	const version = "2026.11.12"

	entriesFor := func(tg target) []archiveEntry {
		return packageEntries(version, tg, bin, root)
	}
	t.Run("zip", func(t *testing.T) {
		tg := target{GOOS: "windows", GOARCH: "amd64", Platform: "windows_amd64", Archive: "zip"}
		dest := filepath.Join(dir, artifactName(version, tg))
		if err := writeArchive(dest, "zip", entriesFor(tg)); err != nil {
			t.Fatal(err)
		}
		zr, err := zip.OpenReader(dest)
		if err != nil {
			t.Fatal(err)
		}
		defer zr.Close()
		got := map[string]string{}
		for _, f := range zr.File {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				t.Fatal(err)
			}
			got[f.Name] = string(data)
		}
		prefix := archiveDir(version, tg)
		if got[prefix+"/sprout.exe"] != "binary" {
			t.Errorf("可执行文件内容不对: %q", got[prefix+"/sprout.exe"])
		}
		if got[prefix+"/README.md"] != "# sprout" {
			t.Errorf("README 内容不对: %q", got[prefix+"/README.md"])
		}
		// Apache-2.0 第 4 条要求分发二进制时附带许可证。
		if got[prefix+"/LICENSE"] != "Apache License" {
			t.Errorf("LICENSE 没有打进包: %q", got[prefix+"/LICENSE"])
		}
	})

	t.Run("targz", func(t *testing.T) {
		tg := target{GOOS: "linux", GOARCH: "amd64", Platform: "linux_amd64", Archive: "targz"}
		dest := filepath.Join(dir, artifactName(version, tg))
		if err := writeArchive(dest, "targz", entriesFor(tg)); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(dest)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		tr := tar.NewReader(gz)
		modes := map[string]int64{}
		contents := map[string]string{}
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			modes[h.Name] = h.Mode
			contents[h.Name] = string(data)
		}
		prefix := archiveDir(version, tg)
		if contents[prefix+"/sprout"] != "binary" {
			t.Errorf("可执行文件内容不对: %q", contents[prefix+"/sprout"])
		}
		// 解压后要能直接运行，靠的就是这个权限位。
		if modes[prefix+"/sprout"]&0o111 == 0 {
			t.Errorf("可执行文件没有执行权限，mode = %o", modes[prefix+"/sprout"])
		}
		if modes[prefix+"/README.md"]&0o111 != 0 {
			t.Errorf("README 不该可执行，mode = %o", modes[prefix+"/README.md"])
		}
		if contents[prefix+"/LICENSE"] != "Apache License" {
			t.Errorf("LICENSE 没有打进包: %q", contents[prefix+"/LICENSE"])
		}
	})
}

// 校验文件只收压缩包，不把 RELEASE.md 和自己算进去（后者会随时变化）。
func TestWriteChecksumsSkipsNotes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sprout_a.zip"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "RELEASE.md"), []byte("# notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	sums, err := writeChecksums(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) != 1 || sums["sprout_a.zip"] == "" {
		t.Fatalf("只应校验压缩包: %v", sums)
	}
	data, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS.txt"))
	if err != nil {
		t.Fatal(err)
	}
	want := sums["sprout_a.zip"] + "  sprout_a.zip\n"
	if string(data) != want {
		t.Fatalf("SHA256SUMS.txt = %q，期望 %q", data, want)
	}
}

// Termux 包必须用 GOOS=android 单独编译。
//
// 用 GOOS=linux 的产物会启动即崩：Android 的 seccomp 策略禁止 faccessat2，
// linux 版的 runtime 先试这个系统调用，被拦下时直接 SIGSYS（用户实测在
// atotto/clipboard 的 init 调 exec.LookPath 时炸掉）；android 版的 runtime
// 跳过它，退回到只带权限位的 faccessat。这条约束一旦回退，问题很难从日志上看出来，
// 所以钉在测试里。
func TestTermuxTargetUsesAndroidGOOS(t *testing.T) {
	for _, tg := range targets() {
		if tg.Platform != "termux_arm64" {
			continue
		}
		if tg.GOOS != "android" {
			t.Errorf("termux_arm64 的 GOOS = %q，必须是 android（linux 会在 Android 上 SIGSYS）", tg.GOOS)
		}
		if tg.GOARCH != "arm64" {
			t.Errorf("termux_arm64 的 GOARCH = %q，期望 arm64", tg.GOARCH)
		}
		return
	}
	t.Error("缺少 termux_arm64 目标")
}

// 自动生成的段落要覆盖到每个实际打出来的包，否则发布说明里的下载表会撒谎。
func TestAutoSectionsListEveryArtifact(t *testing.T) {
	const version = "2026.11.12"
	page := autoSections(version)
	for _, tg := range targets() {
		if !strings.Contains(page, artifactName(version, tg)) {
			t.Errorf("下载表缺少 %s", tg.Platform)
		}
	}
	if !strings.Contains(page, "SHA256SUMS.txt") {
		t.Error("缺少校验说明")
	}
}

func TestStubNotes(t *testing.T) {
	stub := stubNotes("2026.11.12")
	for _, want := range []string{"# Sprout 2026.11.12", "2026 年 11 月 12 日"} {
		if !strings.Contains(stub, want) {
			t.Errorf("草稿里缺少 %q", want)
		}
	}
}

// 单位要和数值对得上：早期版本把 MB 的数值标成 KB，6 MB 的包显示成 "6.2 KB"。
func TestFormatSize(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{900, "900 B"},
		{2048, "2.0 KB"},
		{3 * 1024 * 1024, "3.0 MB"},
		{2 * 1024 * 1024 * 1024, "2.0 GB"},
	}
	for _, c := range cases {
		if got := formatSize(c.n); got != c.want {
			t.Errorf("formatSize(%d) = %q，期望 %q", c.n, got, c.want)
		}
	}
}
