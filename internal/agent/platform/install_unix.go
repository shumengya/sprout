//go:build !windows

package platform

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// InstallCommand 把 exe 注册成全局 sprout 命令，返回给用户看的结果说明。
func InstallCommand(exe string) (string, error) {
	target, err := installTarget(exe)
	if err != nil {
		return "", err
	}
	h, err := newLinkHost()
	if err != nil {
		return "", err
	}
	return h.install(target)
}

// UninstallCommand 卸载 InstallCommand 注册的全局 sprout 命令。
func UninstallCommand() (string, error) {
	h, err := newLinkHost()
	if err != nil {
		return "", err
	}
	return h.uninstall()
}

func newLinkHost() (linkHost, error) {
	dir, err := linkBinDir()
	if err != nil {
		return linkHost{}, err
	}
	return linkHost{fs: osLinkFS{}, binDir: dir, path: os.Getenv("PATH")}, nil
}

// linkBinDir 是放符号链接的目录：Termux 用 $PREFIX/bin（它本来就在 PATH 里），其他用 ~/.local/bin。
func linkBinDir() (string, error) {
	if prefix := os.Getenv("PREFIX"); prefix != "" &&
		(os.Getenv("TERMUX_VERSION") != "" || strings.Contains(prefix, "com.termux")) {
		return filepath.Join(prefix, "bin"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("找不到用户主目录，无法确定 ~/.local/bin: %w", err)
	}
	return filepath.Join(home, ".local", "bin"), nil
}

type osLinkFS struct{}

func (osLinkFS) state(p string) (pathState, string, error) {
	info, err := os.Lstat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return pathMissing, "", nil
	case err != nil:
		return 0, "", fmt.Errorf("无法检查 %s: %w", p, err)
	case info.Mode()&fs.ModeSymlink == 0:
		return pathRegular, "", nil
	}
	target, err := os.Readlink(p)
	if err != nil {
		return 0, "", fmt.Errorf("无法读取符号链接 %s: %w", p, err)
	}
	return pathSymlink, target, nil
}

// symlink 先在同目录建临时链接再 rename 过去，替换旧链接时不会出现“命令短暂消失”的窗口。
func (osLinkFS) symlink(target, link string) error {
	tmp := link + ".sprout-tmp"
	os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func (osLinkFS) remove(p string) error {
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (osLinkFS) mkdirAll(dir string) error { return os.MkdirAll(dir, 0o755) }

func (osLinkFS) isExec(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}
