//go:build unix

package platform

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// ConfigureCmd 让子进程自成进程组，ctx 取消/超时时向整个进程组发 SIGKILL，
// 避免 `bash -c "sleep 999 &"` 这类命令留下孤儿进程占着管道。
func ConfigureCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// 负 PID 表示整个进程组。
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil &&
			!errors.Is(err, syscall.ESRCH) && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		return nil
	}
}
