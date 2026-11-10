package platform

import (
	"fmt"
	"path/filepath"
	"strings"
)

// 这里是 Linux / macOS / Termux 注册方式（符号链接）的核心逻辑。文件系统操作经 linkFS 注入，
// 所以在 Windows 上也能用替身测试；真实实现在 install_unix.go。

type pathState int

const (
	pathMissing pathState = iota
	pathSymlink
	pathRegular // 普通文件或目录：不是我们创建的，不能动
)

type linkFS interface {
	state(p string) (pathState, string, error) // 符号链接时第二个返回值是它指向的路径
	symlink(target, link string) error         // 已存在时原子替换
	remove(p string) error
	mkdirAll(dir string) error
	isExec(p string) bool // p 是存在的可执行文件
}

type linkHost struct {
	fs     linkFS
	binDir string
	path   string // PATH 环境变量
}

func (h linkHost) install(target string) (string, error) {
	link := filepath.Join(h.binDir, "sprout")
	st, old, err := h.fs.state(link)
	if err != nil {
		return "", err
	}
	var lines []string
	switch {
	case st == pathRegular:
		return "", fmt.Errorf("%s 已存在且不是符号链接，没有覆盖它；确认不需要后手动删除再注册", link)
	case st == pathSymlink && old == target:
		lines = append(lines, "sprout 命令已经指向当前程序："+target)
	default:
		if err := h.fs.mkdirAll(h.binDir); err != nil {
			return "", fmt.Errorf("无法创建 %s: %w", h.binDir, err)
		}
		if err := h.fs.symlink(target, link); err != nil {
			return "", fmt.Errorf("无法创建符号链接 %s: %w", link, err)
		}
		lines = append(lines, "已注册全局 sprout 命令："+link+" → "+target)
		if st == pathSymlink {
			lines = append(lines, "原来的 sprout 命令指向 "+old+"，已替换。")
		}
	}
	return strings.Join(append(lines, h.pathNotes(target)...), "\n"), nil
}

// pathNotes 检查 binDir 是否在 PATH 里，以及 PATH 里有没有更靠前的另一个 sprout。不改任何 shell 配置。
func (h linkHost) pathNotes(target string) []string {
	var notes []string
	onPath := false
	for _, d := range filepath.SplitList(h.path) {
		if d == "" {
			continue
		}
		if filepath.Clean(d) == filepath.Clean(h.binDir) {
			onPath = true
			break // 更靠后的 sprout 不会被用到
		}
		if p := filepath.Join(d, "sprout"); h.fs.isExec(p) {
			notes = append(notes, fmt.Sprintf("注意：PATH 里更靠前的 %s 也是 sprout，敲 sprout 会先找到它。", p))
		}
	}
	if !onPath {
		notes = append(notes, h.binDir+" 不在 PATH 里，在 shell 配置文件（如 ~/.bashrc、~/.zshrc）里加一行后重开终端：",
			"  export PATH=\""+h.binDir+":$PATH\"")
	}
	return notes
}

func (h linkHost) uninstall() (string, error) {
	link := filepath.Join(h.binDir, "sprout")
	st, old, err := h.fs.state(link)
	if err != nil {
		return "", err
	}
	switch st {
	case pathMissing:
		return "没有找到已注册的 sprout 命令（" + link + "），无需卸载。", nil
	case pathRegular:
		return "", fmt.Errorf("%s 不是符号链接，不是 /install-sprout 创建的，没有删除它", link)
	}
	if err := h.fs.remove(link); err != nil {
		return "", fmt.Errorf("无法删除 %s: %w", link, err)
	}
	return "已卸载全局 sprout 命令：删除了 " + link + "（原来指向 " + old + "）。程序文件本身没有动。", nil
}
