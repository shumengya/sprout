// Package platform 收敛 shell 探测、控制台初始化、进程树管理等平台差异。
// 其他包不判断 GOOS，只消费这里给出的结果。
package platform

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ShellKind 区分两类终端工具。
type ShellKind string

const (
	KindBash       ShellKind = "bash"
	KindPowerShell ShellKind = "powershell"
)

// Shell 是一次探测到的可用 shell。
type Shell struct {
	Kind ShellKind
	Path string
	// Label 是给人和模型看的名称，如 "Git Bash"、"PowerShell 7+"。
	Label string
	// Edition 仅 PowerShell 使用："7+" 或 "5.1"。
	Edition string
	// Notes 是写进工具描述的平台补充说明，如 Windows 上 bash 的路径写法。
	Notes string
}

// Problem 说明某个 shell 为什么不可用。
type Problem struct {
	Kind   ShellKind
	Reason string
}

// Shells 是探测结果：Bash / PowerShell 为 nil 表示不可用，原因在 Problems 里。
type Shells struct {
	Bash       *Shell
	PowerShell *Shell
	Problems   []Problem
}

// Probe 抽象文件系统与环境变量，探测逻辑据此可以脱离真实系统测试。
type Probe struct {
	// Exists 判断路径是否是一个存在的文件。
	Exists func(path string) bool
	// Getenv 读取环境变量。
	Getenv func(key string) string
	// PathDirs 返回 PATH 中的目录列表。
	PathDirs func() []string
}

// OSProbe 返回基于真实系统的 Probe。
func OSProbe() Probe {
	return Probe{
		Exists: func(p string) bool {
			st, err := os.Stat(p)
			return err == nil && !st.IsDir()
		},
		Getenv: os.Getenv,
		PathDirs: func() []string {
			return filepath.SplitList(os.Getenv("PATH"))
		},
	}
}

// findInPath 按 PATH 顺序查找 names 中的第一个存在项，skip 返回 true 的结果被跳过。
func (p Probe) findInPath(names []string, skip func(path string) bool) string {
	for _, dir := range p.PathDirs() {
		if dir == "" {
			continue
		}
		for _, n := range names {
			full := filepath.Join(dir, n)
			if !p.Exists(full) {
				continue
			}
			if skip != nil && skip(full) {
				continue
			}
			return full
		}
	}
	return ""
}

// OSName 返回给模型看的操作系统名称。
func OSName() string {
	switch runtime.GOOS {
	case "windows":
		return "Windows"
	case "darwin":
		return "macOS"
	case "android":
		return "Android (Termux)"
	case "linux":
		return "Linux"
	}
	return runtime.GOOS
}

// pwshEdition 由可执行文件名判断 PowerShell 版本：pwsh 为 7+，其余按 5.1 保守处理。
func pwshEdition(path string) string {
	base := strings.ToLower(filepath.Base(path))
	if strings.HasPrefix(base, "pwsh") {
		return "7+"
	}
	return "5.1"
}
