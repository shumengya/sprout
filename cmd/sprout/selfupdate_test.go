package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLookupChecksum(t *testing.T) {
	sums := []byte("aaaa  sprout_1_windows_amd64.zip\nbbbb  *sprout_1_linux_amd64.tar.gz\n\nbad line here\n")
	if got, ok := lookupChecksum(sums, "sprout_1_windows_amd64.zip"); !ok || got != "aaaa" {
		t.Fatalf("得到 %q %v", got, ok)
	}
	// 二进制模式的行前面带 *，不能因此找不到。
	if got, ok := lookupChecksum(sums, "sprout_1_linux_amd64.tar.gz"); !ok || got != "bbbb" {
		t.Fatalf("得到 %q %v", got, ok)
	}
	if _, ok := lookupChecksum(sums, "sprout_2.zip"); ok {
		t.Error("不存在文件不该命中")
	}
}

// makeZip 造一个和 tools/release 同布局的压缩包：顶层目录 + 程序文件。
func makeZip(t *testing.T, dir, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, err := zw.Create(dir + "/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeTarGz(t *testing.T, dir, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: dir + "/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: dir + "/" + name, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractBinaryZipAndTarGz(t *testing.T) {
	body := []byte("new binary")
	z := makeZip(t, "sprout_1_windows_amd64", "sprout.exe", body)
	if got, err := extractBinary("sprout_1_windows_amd64.zip", z, "sprout.exe"); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("zip: %q %v", got, err)
	}
	tg := makeTarGz(t, "sprout_1_linux_amd64", "sprout", body)
	if got, err := extractBinary("sprout_1_linux_amd64.tar.gz", tg, "sprout"); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("tar.gz: %q %v", got, err)
	}
	// 只要顶层目录里的那一个，深层路径与同名文件都不认。
	if _, err := extractBinary("x.zip", z, "other.exe"); err == nil {
		t.Error("找不到程序文件时应报错")
	}
	if _, err := extractBinary("x.zip", []byte("not an archive"), "sprout.exe"); err == nil {
		t.Error("损坏的压缩包应报错")
	}
}

// fakeRelease 起一个同时返回发布信息和产物的服务端，并让 releaseURL 指向它。
func fakeRelease(t *testing.T, tag string, archive []byte, sums bool) *updatePlan {
	t.Helper()
	assetName := "sprout_" + tag + "_" + platformName() + ".zip"
	sum := sha256.Sum256(archive)
	sumsBody := hex.EncodeToString(sum[:]) + "  " + assetName + "\n"
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			assets := []map[string]any{{"name": assetName, "browser_download_url": srv.URL + "/dl/" + assetName}}
			if sums {
				assets = append(assets, map[string]any{"name": checksumsName, "browser_download_url": srv.URL + "/dl/" + checksumsName})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": tag, "html_url": "https://example/tag", "assets": assets})
		case strings.HasSuffix(r.URL.Path, "/"+checksumsName):
			_, _ = w.Write([]byte(sumsBody))
		case strings.HasSuffix(r.URL.Path, "/"+assetName):
			_, _ = w.Write(archive)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	orig := releaseURL
	releaseURL = func(repo string) string { return srv.URL + "/repos/" + repo + "/releases/latest" }
	t.Cleanup(func() { releaseURL = orig })

	p, err := checkUpdate("2026.11.01", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestInstallUpdateReplacesExecutable(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "sprout.exe")
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	archive := makeZip(t, "sprout_2026.11.20_"+platformName(), filepath.Base(exe), []byte("new binary"))
	p := fakeRelease(t, "2026.11.20", archive, true)

	var steps []string
	oldSmoke := smokeTest
	smokeTest = func(context.Context, string) error { return nil }
	t.Cleanup(func() { smokeTest = oldSmoke })

	if err := installUpdate(context.Background(), p, exe, nil, func(s string) { steps = append(steps, s) }); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(exe)
	if err != nil || string(got) != "new binary" {
		t.Fatalf("程序应被替换，得到 %q %v", got, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		// Windows 上旧程序被改名成 .old 留在旁边，下次启动时清理。
		if e.Name() != filepath.Base(exe) && e.Name() != filepath.Base(exe)+".old" {
			t.Errorf("不该留下临时文件 %s", e.Name())
		}
	}
	if len(steps) == 0 {
		t.Error("应报告进度")
	}
}

func TestInstallUpdateKeepsProgramOnMismatch(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "sprout.exe")
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	archive := makeZip(t, "sprout_2026.11.20_"+platformName(), filepath.Base(exe), []byte("new binary"))
	p := fakeRelease(t, "2026.11.20", archive, true)

	// 校验值对不上：内容被换过，绝不能替换。
	sum := sha256.Sum256([]byte("something else"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(hex.EncodeToString(sum[:]) + "  " + p.AssetName + "\n"))
	}))
	t.Cleanup(srv.Close)
	p.ChecksumsURL = srv.URL

	oldSmoke := smokeTest
	smokeTest = func(context.Context, string) error { return nil }
	t.Cleanup(func() { smokeTest = oldSmoke })

	err := installUpdate(context.Background(), p, exe, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "校验失败") {
		t.Fatalf("应报校验失败，得到 %v", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old binary" {
		t.Fatalf("原程序不该被动过，得到 %q", got)
	}
}

func TestInstallUpdateRequiresChecksums(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "sprout.exe")
	archive := makeZip(t, "sprout_2026.11.20_"+platformName(), filepath.Base(exe), []byte("new"))
	p := fakeRelease(t, "2026.11.20", archive, false)
	err := installUpdate(context.Background(), p, exe, nil, nil)
	if err == nil || !strings.Contains(err.Error(), checksumsName) {
		t.Fatalf("没有校验文件时应拒绝替换，得到 %v", err)
	}
}

func TestUpdateCheckOnlyDoesNotInstall(t *testing.T) {
	archive := makeZip(t, "sprout_2026.11.20_"+platformName(), "sprout.exe", []byte("new"))
	fakeRelease(t, "2026.11.20", archive, true)
	code, out, errOut := runUpdateCheck("2026.11.01", "", true)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "有新版本可用") || !strings.Contains(out, "--check") {
		t.Fatalf("--check 应只报告:\n%s", out)
	}
	if strings.Contains(out, "已更新到") {
		t.Fatalf("--check 不能替换程序:\n%s", out)
	}
}

func TestUpdateSkipsCheckForDevBuild(t *testing.T) {
	if s := (autoUpdater{version: "dev"}).Check(); s != "" {
		t.Fatalf("开发构建不该联网检查，得到 %q", s)
	}
}

func TestAutoUpdaterCheckReportsNewRelease(t *testing.T) {
	archive := makeZip(t, "sprout_2026.11.20_"+platformName(), "sprout.exe", []byte("new"))
	fakeRelease(t, "2026.11.20", archive, true)
	s := (autoUpdater{version: "2026.11.01"}).Check()
	if !strings.Contains(s, "有新版本可用：2026.11.20") || !strings.Contains(s, "/update") {
		t.Fatalf("提示 = %q", s)
	}
	fakeRelease(t, "2026.11.01", archive, true)
	if s := (autoUpdater{version: "2026.11.01"}).Check(); s != "" {
		t.Fatalf("已是最新时不该提示，得到 %q", s)
	}
}
