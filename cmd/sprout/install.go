package main

import (
	"fmt"
	"os"

	"github.com/shumengya/sprout/internal/agent/platform"
)

// commandInstaller 把平台层的注册实现接到 TUI 的 /install-sprout、/uninstall-sprout 上。
// 注册的是当前正在运行的这个程序。
type commandInstaller struct{}

func (commandInstaller) Install() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("无法定位当前程序: %w", err)
	}
	return platform.InstallCommand(exe)
}

func (commandInstaller) Uninstall() (string, error) { return platform.UninstallCommand() }
