// Command release 构建 Sprout 的发布包。
//
// 产物写在 <out>/<版本号>/ 下：每个平台一个压缩包（Windows 用 zip，其余用 tar.gz）、
// 一份 SHA256SUMS.txt 和一份 RELEASE.md。RELEASE.md 由 docs/releases/<版本号>.md 的正文
// 加上这里自动生成的下载表组成，所以发布说明只需要手写人读的部分。
//
// 版本号取日期（2026.11.12），可带后缀（2026.11.12-rc.1）。
package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// versionLayout 与命令行 --version、git 标签一致，日期版本号不带前导 v。
const versionLayout = "2006.01.02"

func main() {
	version := flag.String("version", "", "版本号，默认当天日期，如 2026.11.12")
	out := flag.String("out", "release", "输出目录，相对路径以仓库根为基准")
	notes := flag.String("notes", "", "发布说明正文，默认 docs/releases/<版本号>.md")
	check := flag.Bool("check", true, "构建前依次运行 go build / go vet / go test")
	flag.Parse()

	v := *version
	if v == "" {
		v = time.Now().Format(versionLayout)
	}
	if err := run(v, *out, *notes, *check); err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func run(version, out, notesPath string, check bool) error {
	if !validVersion(version) {
		return fmt.Errorf("版本号 %q 不合法：用日期，如 2026.11.12（可加后缀，如 -rc.1）", version)
	}
	root, err := moduleRoot()
	if err != nil {
		return err
	}
	if check {
		if err := runChecks(root); err != nil {
			return err
		}
	}

	dir := out
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	dir = filepath.Join(dir, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	tmp, err := os.MkdirTemp("", "sprout-release-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	fmt.Printf("\n版本 %s\n输出 %s\n\n", version, dir)
	for _, t := range targets() {
		bin := filepath.Join(tmp, t.Platform, t.binaryName())
		if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
			return err
		}
		if err := buildTarget(root, version, t, bin); err != nil {
			return err
		}
		name := artifactName(version, t)
		entries := packageEntries(version, t, bin, root)
		if err := writeArchive(filepath.Join(dir, name), t.Archive, entries); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		fmt.Printf("  %-45s %s\n", name, humanSize(filepath.Join(dir, name)))
	}

	if _, err := writeChecksums(dir); err != nil {
		return err
	}
	body, err := readNotes(root, version, notesPath)
	if err != nil {
		return err
	}
	page := body + "\n\n" + autoSections(version)
	if err := os.WriteFile(filepath.Join(dir, "RELEASE.md"), []byte(page), 0o644); err != nil {
		return err
	}
	fmt.Printf("  %-45s %s\n", "SHA256SUMS.txt", humanSize(filepath.Join(dir, "SHA256SUMS.txt")))
	fmt.Printf("  %-45s %s\n", "RELEASE.md", humanSize(filepath.Join(dir, "RELEASE.md")))
	return nil
}

// target 是一个发布目标。GOARM 只对 32 位 ARM 有意义。
type target struct {
	GOOS, GOARCH, GOARM string
	Platform            string // 文件名与解压目录用的平台名，如 linux_armv7
	Label               string // 发布说明里给人看的平台名
	Archive             string // zip / targz
}

func targets() []target {
	return []target{
		{GOOS: "windows", GOARCH: "amd64", Platform: "windows_amd64", Label: "Windows x64", Archive: "zip"},
		{GOOS: "windows", GOARCH: "arm64", Platform: "windows_arm64", Label: "Windows ARM64", Archive: "zip"},
		{GOOS: "linux", GOARCH: "amd64", Platform: "linux_amd64", Label: "Linux x64", Archive: "targz"},
		{GOOS: "linux", GOARCH: "arm64", Platform: "linux_arm64", Label: "Linux ARM64", Archive: "targz"},
		{GOOS: "linux", GOARCH: "arm", GOARM: "7", Platform: "linux_armv7", Label: "Linux ARMv7", Archive: "targz"},
		{GOOS: "darwin", GOARCH: "amd64", Platform: "darwin_amd64", Label: "macOS Intel", Archive: "targz"},
		{GOOS: "darwin", GOARCH: "arm64", Platform: "darwin_arm64", Label: "macOS Apple 芯片", Archive: "targz"},
		// Termux 必须用 GOOS=android，不能复用 linux_arm64：
		// Android 的 seccomp 策略禁止 faccessat2，而 GOOS=linux 的 runtime 会先试它，
		// 被拦下时进程直接收到 SIGSYS 而不是 ENOSYS，启动即崩。GOOS=android 的
		// runtime 完全跳过这个系统调用（syscall.Faccessat 里 runtime.GOOS != "android"），
		// internal/syscall/unix.Eaccess 也直接返回 ENOSYS 让调用方回退。
		// CGO_ENABLED=0 下产物是不依赖 bionic 的静态 ELF，在 Termux 里可直接运行。
		{GOOS: "android", GOARCH: "arm64", Platform: "termux_arm64", Label: "Android Termux（ARM64）", Archive: "targz"},
	}
}

// binaryName 只由 GOOS 决定。
func (t target) binaryName() string {
	if t.GOOS == "windows" {
		return "sprout.exe"
	}
	return "sprout"
}

// binaryMode 决定解压后是否可执行；Windows 不读文件权限，统一 0644。
func (t target) binaryMode() fs.FileMode {
	if t.GOOS == "windows" {
		return 0o644
	}
	return 0o755
}

func artifactName(version string, t target) string {
	ext := ".tar.gz"
	if t.Archive == "zip" {
		ext = ".zip"
	}
	return fmt.Sprintf("sprout_%s_%s%s", version, t.Platform, ext)
}

// archiveDir 是压缩包内的顶层目录，和压缩包同名（去掉扩展名），解压后不会散落一地。
func archiveDir(version string, t target) string {
	return fmt.Sprintf("sprout_%s_%s", version, t.Platform)
}

var versionRe = regexp.MustCompile(`^\d{4}\.\d{2}\.\d{2}(-[0-9A-Za-z][0-9A-Za-z.]*)?$`)

// validVersion 要求日期部分真是合法日期：2026.02.30 这种拼错的版本号发布出去收不回来。
func validVersion(v string) bool {
	if !versionRe.MatchString(v) {
		return false
	}
	date, _, _ := strings.Cut(v, "-")
	_, err := time.Parse(versionLayout, date)
	return err == nil
}

// moduleRoot 返回包含 go.mod 的仓库根：用户可能在任意子目录执行，发布产物不该跟着 cwd 跑。
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("找不到 go.mod，请在 sprout 仓库里运行")
		}
		dir = parent
	}
}

func runChecks(root string) error {
	for _, args := range [][]string{{"build", "./..."}, {"vet", "./..."}, {"test", "./..."}} {
		fmt.Println("go", strings.Join(args, " "))
		cmd := exec.Command("go", args...)
		cmd.Dir = root
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("go %s 失败，先修好再发布: %w", args[0], err)
		}
	}
	fmt.Println()
	return nil
}

func buildTarget(root, version string, t target, dest string) error {
	cmd := exec.Command("go", "build", "-trimpath",
		"-ldflags", "-s -w -X main.version="+version, "-o", dest, "./cmd/sprout")
	cmd.Dir = root
	cmd.Env = releaseEnv(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("编译 %s 失败: %w\n%s", t.Platform, err, out)
	}
	return nil
}

// releaseEnv 在父进程环境之后追加交叉编译变量，同名变量后出现的生效，
// 所以父进程里已有的 GOOS / GOARCH 不会污染发布构建。
func releaseEnv(t target) []string {
	env := append(os.Environ(), "CGO_ENABLED=0", "GOOS="+t.GOOS, "GOARCH="+t.GOARCH)
	if t.GOARM != "" {
		env = append(env, "GOARM="+t.GOARM)
	}
	return env
}

// packageEntries 是每个压缩包的内容：可执行文件、README，以及 LICENSE。
// 分发二进制必须一并给出许可证与版权声明（Apache-2.0 第 4 条）。
func packageEntries(version string, t target, bin, root string) []archiveEntry {
	dir := archiveDir(version, t)
	return []archiveEntry{
		{Name: dir + "/" + t.binaryName(), Path: bin, Mode: t.binaryMode()},
		{Name: dir + "/README.md", Path: filepath.Join(root, "README.md"), Mode: 0o644},
		{Name: dir + "/LICENSE", Path: filepath.Join(root, "LICENSE"), Mode: 0o644},
	}
}

// archiveEntry 是压缩包里的一个文件。
type archiveEntry struct {
	Name string // 包内路径，用 / 分隔
	Path string // 磁盘上的源文件
	Mode fs.FileMode
}

func writeArchive(dest, format string, entries []archiveEntry) (err error) {
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	switch format {
	case "zip":
		return writeZip(f, entries)
	case "targz":
		return writeTarGz(f, entries)
	}
	return fmt.Errorf("未知的压缩格式 %q", format)
}

func writeZip(w io.Writer, entries []archiveEntry) error {
	zw := zip.NewWriter(w)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.Name, Method: zip.Deflate}
		h.SetMode(e.Mode)
		h.Modified = time.Now()
		dst, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		if err := copyFile(dst, e.Path); err != nil {
			return err
		}
	}
	return zw.Close()
}

func writeTarGz(w io.Writer, entries []archiveEntry) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		info, err := os.Stat(e.Path)
		if err != nil {
			return err
		}
		h := &tar.Header{
			Name: e.Name, Mode: int64(e.Mode), Size: info.Size(),
			ModTime: time.Now(), Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if err := copyFile(tw, e.Path); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func copyFile(dst io.Writer, path string) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	_, err = io.Copy(dst, src)
	return err
}

// writeChecksums 为目录里除 RELEASE.md 与自身之外的文件写 SHA256SUMS.txt，按文件名排序。
func writeChecksums(dir string) (map[string]string, error) {
	items, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	sums := map[string]string{}
	var names []string
	for _, it := range items {
		name := it.Name()
		if it.IsDir() || name == "SHA256SUMS.txt" || name == "RELEASE.md" {
			continue
		}
		sum, err := fileSHA256(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		sums[name] = sum
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "%s  %s\n", sums[n], n)
	}
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS.txt"), []byte(b.String()), 0o644); err != nil {
		return nil, err
	}
	return sums, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// readNotes 读发布说明正文；缺失时写一份草稿再继续，
// 这样一条命令能跑完，人只要回头补内容。
func readNotes(root, version, path string) (string, error) {
	if path == "" {
		path = filepath.Join(root, "docs", "releases", version+".md")
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	data, err := os.ReadFile(path)
	if err == nil {
		return strings.TrimRight(string(data), "\n"), nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	stub := stubNotes(version)
	if err := os.WriteFile(path, []byte(stub), 0o644); err != nil {
		return "", err
	}
	fmt.Printf("提示: 发布说明还不存在，已生成草稿 %s，请补充后重新运行\n\n", path)
	return strings.TrimRight(stub, "\n"), nil
}

func stubNotes(version string) string {
	return "# Sprout " + version + "\n\n" + releaseDate(version) + "发布。\n\n" +
		"## 新增\n\n- 待补充\n\n## 修复\n\n- 待补充\n"
}

// releaseDate 把版本号里的日期写成中文，版本号带后缀时去掉后缀。
func releaseDate(version string) string {
	date, _, _ := strings.Cut(version, "-")
	t, err := time.Parse(versionLayout, date)
	if err != nil {
		return date
	}
	return fmt.Sprintf("%d 年 %d 月 %d 日", t.Year(), t.Month(), t.Day())
}

// autoSections 生成下载表和一两行必要的说明。平台清单直接来自 targets()，
// 不会出现“发布说明里写了但实际没打这个包”的情况。
// 刻意写短：安装、校验、源码构建在 README 和 docs/release.md 里都有，发布页上只留选包需要的信息。
func autoSections(version string) string {
	var b strings.Builder
	b.WriteString("## 下载\n\n| 平台 | 文件 |\n| --- | --- |\n")
	for _, t := range targets() {
		fmt.Fprintf(&b, "| %s | `%s` |\n", t.Label, artifactName(version, t))
	}
	b.WriteString("\n校验和见 `SHA256SUMS.txt`。Termux 请用 `termux_arm64` 包，并放在 home 目录下运行（`/sdcard` 上不能执行程序）。\n")
	return b.String()
}

func humanSize(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return formatSize(info.Size())
}

// formatSize 按 1024 进制给出 KB / MB，用于控制台汇总。
func formatSize(n int64) string {
	const unit = 1024
	switch {
	case n >= unit*unit*unit:
		return fmt.Sprintf("%.1f GB", float64(n)/unit/unit/unit)
	case n >= unit*unit:
		return fmt.Sprintf("%.1f MB", float64(n)/unit/unit)
	case n >= unit:
		return fmt.Sprintf("%.1f KB", float64(n)/unit)
	}
	return fmt.Sprintf("%d B", n)
}
