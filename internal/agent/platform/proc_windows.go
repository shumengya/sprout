//go:build windows

package platform

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

// createNoWindow 避免 taskkill 在控制台程序里闪出新窗口。
const createNoWindow = 0x08000000

// ConfigureCmd 让工具进程使用自己的隐藏控制台，并在 ctx 取消/超时时终止整个进程树。
//
// 隐藏控制台：默认情况下子进程共享 sprout 的控制台。我们只接管了 stdout/stderr 管道，
// 孙进程（如 Start-Process 拉起的 Chrome 打印 "DevTools listening on ..."）直接写控制台时
// 会绕过管道画到 TUI 上，也能读到 TUI 的键盘输入。CREATE_NO_WINDOW 给它们一个看不见的独立控制台。
//
// 进程树：否则 `pwsh -Command "ping ..."` 这类命令超时后孙进程会继续跑并占着管道。
func ConfigureCmd(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		taskkill := "taskkill"
		if root := os.Getenv("SystemRoot"); root != "" {
			taskkill = filepath.Join(root, "System32", "taskkill.exe")
		}
		kill := exec.Command(taskkill, "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
		kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
		_ = kill.Run()
		// taskkill 失败时退回到只杀直接子进程；进程已退出不算错误。
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		return nil
	}
}
