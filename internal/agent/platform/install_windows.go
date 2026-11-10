//go:build windows

package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// winHost 是 Windows 注册流程依赖的外部世界。函数字段在测试里换成替身，不碰真实注册表。
type winHost struct {
	binDir    string // 放 sprout.cmd 的目录，也是加进 PATH 的那一条
	backupDir string // 改 PATH 前的备份放这里

	readUser   func() (string, bool, error)
	writeUser  func(v string, expand bool) error
	readSystem func() string
	broadcast  func()
	expand     func(string) string
	dirInfo    func(dir string) (hasSprout, onlySprout bool)
	short      func(string) string
	now        func() time.Time
}

func newWinHost() (winHost, error) {
	base, err := os.UserCacheDir() // %LOCALAPPDATA%
	if err != nil {
		return winHost{}, fmt.Errorf("找不到 %%LOCALAPPDATA%%，无法确定安装目录: %w", err)
	}
	root := filepath.Join(base, "sprout")
	exts := exeExts(os.Getenv("PATHEXT"))
	return winHost{
		binDir: filepath.Join(root, "bin"), backupDir: root,
		readUser: readUserPath, writeUser: writeUserPath, readSystem: readSystemPath, broadcast: broadcastEnvChange,
		expand:  func(s string) string { return expandWin(s, os.LookupEnv) },
		dirInfo: func(d string) (bool, bool) { return sproutDirInfo(d, exts) },
		short:   shortPath, now: time.Now,
	}, nil
}

// InstallCommand 把 exe 注册成全局 sprout 命令，返回给用户看的结果说明。
func InstallCommand(exe string) (string, error) {
	target, err := installTarget(exe)
	if err != nil {
		return "", err
	}
	h, err := newWinHost()
	if err != nil {
		return "", err
	}
	return h.install(target)
}

// UninstallCommand 卸载 InstallCommand 注册的全局 sprout 命令。
func UninstallCommand() (string, error) {
	h, err := newWinHost()
	if err != nil {
		return "", err
	}
	return h.uninstall()
}

func (h winHost) install(target string) (string, error) {
	// 先把所有检查做完再动手，任何一步不通过都不留下半截的状态。
	call := target
	if !isASCII(call) {
		// .cmd 按控制台代码页解析，非 ASCII 路径在不同代码页下会乱码；改用纯 ASCII 的 8.3 短路径。
		if call = h.short(target); !isASCII(call) {
			return "", fmt.Errorf("程序路径 %s 含非 ASCII 字符，且这个卷没有短文件名可用；请把程序放到纯英文路径下再注册", target)
		}
	}
	shim := filepath.Join(h.binDir, "sprout.cmd")
	if _, err := ownShim(shim); err != nil {
		return "", err
	}
	old, expand, err := h.readUser()
	if err != nil {
		return "", err
	}
	plan := planInstallPath(old, h.binDir, filepath.Dir(target), h.expand, h.dirInfo)

	if err := os.MkdirAll(h.binDir, 0o755); err != nil {
		return "", fmt.Errorf("无法创建 %s: %w", h.binDir, err)
	}
	if err := writeAtomic(shim, shimContent(call)); err != nil {
		return "", fmt.Errorf("无法写入 %s: %w", shim, err)
	}
	var backup string
	if plan.changed {
		if backup, err = h.backup(old); err != nil {
			return "", err
		}
		if err := h.writeUser(strings.Join(plan.entries, ";"), expand); err != nil {
			return "", err
		}
		h.broadcast()
	}
	return h.installReport(target, shim, plan, backup), nil
}

func (h winHost) installReport(target, shim string, plan pathPlan, backup string) string {
	lines := []string{"已注册全局 sprout 命令，指向 " + target, "  启动脚本：" + shim}
	if plan.changed {
		lines = append(lines, "  已把 "+h.binDir+" 放到用户 PATH 最前面")
	} else {
		lines = append(lines, "  用户 PATH 已经是最新的，没有改动")
	}
	for _, d := range plan.removed {
		lines = append(lines, "  已从用户 PATH 移除只装着旧 sprout 的目录："+d+"（目录里的文件没有动）")
	}
	for _, d := range plan.kept {
		lines = append(lines, "  注意："+d+" 里也有 sprout，但还有别的程序，没有从 PATH 移除；新的 sprout 排在它前面，不受影响。")
	}
	for _, d := range strings.Split(h.readSystem(), ";") {
		x := h.expand(d)
		if strings.TrimSpace(x) == "" || sameWinPath(x, h.binDir) || sameWinPath(x, filepath.Dir(target)) {
			continue
		}
		if has, _ := h.dirInfo(x); has {
			lines = append(lines, "  注意：系统 PATH 里的 "+d+" 也有 sprout。Windows 先查系统 PATH，敲 sprout 仍会运行它；"+
				"改系统 PATH 需要管理员权限，请手动处理。")
		}
	}
	if backup != "" {
		lines = append(lines, "  修改前的用户 PATH 已备份到 "+backup)
	}
	return strings.Join(append(lines, "新开的终端里生效；已经打开的终端需要重开。"), "\n")
}

func (h winHost) uninstall() (string, error) {
	shim := filepath.Join(h.binDir, "sprout.cmd")
	hasShim, err := ownShim(shim)
	if err != nil {
		return "", err
	}
	old, expand, err := h.readUser()
	if err != nil {
		return "", err
	}
	next, onPath := stripPath(old, h.binDir, h.expand)
	if !hasShim && !onPath {
		return "没有找到已注册的 sprout 命令，无需卸载。", nil
	}
	lines := []string{"已卸载全局 sprout 命令。程序文件本身没有动。"}
	if onPath {
		backup, err := h.backup(old)
		if err != nil {
			return "", err
		}
		if err := h.writeUser(next, expand); err != nil {
			return "", err
		}
		h.broadcast()
		lines = append(lines, "  已从用户 PATH 移除 "+h.binDir, "  修改前的用户 PATH 已备份到 "+backup)
	}
	if hasShim {
		if err := os.Remove(shim); err != nil {
			return "", fmt.Errorf("PATH 已改好，但无法删除 %s: %w", shim, err)
		}
		os.Remove(h.binDir) // 只在目录已空时成功
		lines = append(lines, "  已删除 "+shim)
	}
	return strings.Join(append(lines, "新开的终端里生效。"), "\n"), nil
}

// backup 在改 PATH 之前把原值存一份，改坏了可以照着恢复。
func (h winHost) backup(old string) (string, error) {
	if err := os.MkdirAll(h.backupDir, 0o755); err != nil {
		return "", fmt.Errorf("无法创建备份目录 %s: %w", h.backupDir, err)
	}
	p := filepath.Join(h.backupDir, "path-backup-"+h.now().Format("20060102-150405.000")+".txt")
	if err := os.WriteFile(p, []byte(old), 0o600); err != nil {
		return "", fmt.Errorf("备份用户 PATH 失败，没有修改 PATH: %w", err)
	}
	return p, nil
}

// writeAtomic 先写临时文件再改名，写到一半失败不会留下半截的 sprout.cmd。
func writeAtomic(p, content string) error {
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
