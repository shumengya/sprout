package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 全局 sprout 命令的注册与卸载（TUI 的 /install-sprout、/uninstall-sprout）。
//
// 无论哪个平台都是“原地调用当前这个 exe”，不复制它：数据目录跟随 exe（<exe目录>/data/.sprout），
// 复制一份到别处就等于换了一个数据目录，配置和会话都会“丢”。
//   - Windows：在 %LOCALAPPDATA%\sprout\bin 放 sprout.cmd 指向 exe，并把这个目录放到用户 PATH 最前面；
//     用户 PATH 里只装着旧 sprout 的目录一并移除（目录里还有别的程序时不动，只提示）。
//   - Linux / macOS：~/.local/bin/sprout 符号链接；Termux：$PREFIX/bin/sprout。不改 shell 配置文件，
//     目录不在 PATH 里时提示怎么加。

// installTarget 校验并返回要注册的 exe 的真实路径。
func installTarget(exe string) (string, error) {
	abs, err := filepath.Abs(exe)
	if err != nil {
		return "", fmt.Errorf("无法确定程序路径 %q: %w", exe, err)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	for _, part := range strings.Split(filepath.ToSlash(abs), "/") {
		if strings.HasPrefix(part, "go-build") {
			return "", fmt.Errorf("当前程序是 go run 生成的临时文件（%s），注册后很快就会失效；请先 go build 出正式的可执行文件再注册", abs)
		}
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("找不到程序 %s: %w", abs, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s 是目录，不是可执行文件", abs)
	}
	return abs, nil
}
