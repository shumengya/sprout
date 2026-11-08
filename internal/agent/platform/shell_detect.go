package platform

import (
	"fmt"
	"path/filepath"
	"strings"
)

// 探测逻辑本身不带 build tag，只依赖 Probe，方便在任何平台上测试；
// shell_windows.go / shell_unix.go 只负责选择调用哪一个。

const (
	wslHint = "只找到 WSL 启动器（System32\\bash.exe），它不是 Git Bash；请安装 Git for Windows，或在 models.jsonc 的 shell.bashPath 填入 bash.exe 绝对路径"
	noBash  = "未找到 Git Bash；请安装 Git for Windows，或在 models.jsonc 的 shell.bashPath 填入 bash.exe 绝对路径"
)

// isWSLLauncher 判断路径是否是 WSL 启动器：System32 / Sysnative 下的 bash.exe，或 WindowsApps 别名。
func isWSLLauncher(path string) bool {
	p := strings.ToLower(strings.ReplaceAll(path, `\`, "/"))
	return strings.HasSuffix(p, "/windows/system32/bash.exe") ||
		strings.HasSuffix(p, "/windows/sysnative/bash.exe") ||
		strings.Contains(p, "/windowsapps/")
}

// detectWindows 按文档约定的顺序探测 Windows 上的 PowerShell 与 Git Bash。
func detectWindows(cfgBash, cfgPS string, p Probe) Shells {
	var s Shells
	if sh, why := detectWindowsPowerShell(cfgPS, p); sh != nil {
		s.PowerShell = sh
	} else {
		s.Problems = append(s.Problems, Problem{KindPowerShell, why})
	}
	if sh, why := detectWindowsBash(cfgBash, p); sh != nil {
		s.Bash = sh
	} else {
		s.Problems = append(s.Problems, Problem{KindBash, why})
	}
	return s
}

func newPowerShell(path string) *Shell {
	ed := pwshEdition(path)
	label := "PowerShell 5.1"
	if ed == "7+" {
		label = "PowerShell 7+"
	}
	return &Shell{Kind: KindPowerShell, Path: path, Label: label, Edition: ed}
}

func detectWindowsPowerShell(configured string, p Probe) (*Shell, string) {
	// 显式配置的路径无效时直接报错，不悄悄换成别的 shell。
	if configured != "" {
		if !p.Exists(configured) {
			return nil, fmt.Sprintf("shell.powershellPath 指向的文件不存在: %s", configured)
		}
		return newPowerShell(configured), ""
	}
	if path := p.findInPath([]string{"pwsh.exe"}, nil); path != "" {
		return newPowerShell(path), ""
	}
	if pf := p.Getenv("ProgramFiles"); pf != "" {
		if c := filepath.Join(pf, "PowerShell", "7", "pwsh.exe"); p.Exists(c) {
			return newPowerShell(c), ""
		}
	}
	if path := p.findInPath([]string{"powershell.exe"}, nil); path != "" {
		return newPowerShell(path), ""
	}
	if sr := p.Getenv("SystemRoot"); sr != "" {
		c := filepath.Join(sr, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		if p.Exists(c) {
			return newPowerShell(c), ""
		}
	}
	return nil, "未找到 pwsh.exe 或 powershell.exe"
}

// newBash 构造 Windows 上的 bash（Git Bash / MSYS2 / Cygwin），路径写法与 Windows 不同。
func newBash(path, label string) *Shell {
	return &Shell{
		Kind: KindBash, Path: path, Label: label,
		Notes: "运行在 Windows 上，路径使用 POSIX 形式（如 /d/SmyProjects/foo），不要写 D:\\SmyProjects\\foo。",
	}
}

// newUnixBash 构造 Linux / macOS / Termux 上的 bash。
func newUnixBash(path string) *Shell {
	return &Shell{Kind: KindBash, Path: path, Label: "bash"}
}

func detectWindowsBash(configured string, p Probe) (*Shell, string) {
	if configured != "" {
		if !p.Exists(configured) {
			return nil, fmt.Sprintf("shell.bashPath 指向的文件不存在: %s", configured)
		}
		if isWSLLauncher(configured) {
			return nil, wslHint
		}
		return newBash(configured, "Git Bash"), ""
	}
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
		if root := p.Getenv(env); root != "" {
			if c := filepath.Join(root, "Git", "bin", "bash.exe"); p.Exists(c) {
				return newBash(c, "Git Bash"), ""
			}
		}
	}
	if path := p.findInPath([]string{"bash.exe"}, isWSLLauncher); path != "" {
		return newBash(path, "Bash (MSYS2/Cygwin)"), ""
	}
	// 区分“只有 WSL 启动器”和“什么都没有”，给出更准确的提示。
	if p.findInPath([]string{"bash.exe"}, nil) != "" {
		return nil, wslHint
	}
	return nil, noBash
}

// detectUnix 探测 Linux / macOS / Termux：只有 bash，PowerShell 不注册，也不算问题。
func detectUnix(cfgBash string, p Probe) Shells {
	var s Shells
	if cfgBash != "" {
		if p.Exists(cfgBash) {
			s.Bash = newUnixBash(cfgBash)
		} else {
			s.Problems = append(s.Problems, Problem{KindBash, fmt.Sprintf("shell.bashPath 指向的文件不存在: %s", cfgBash)})
		}
		return s
	}
	if path := p.findInPath([]string{"bash"}, nil); path != "" {
		s.Bash = newUnixBash(path)
		return s
	}
	s.Problems = append(s.Problems, Problem{KindBash, "PATH 中未找到 bash"})
	return s
}
