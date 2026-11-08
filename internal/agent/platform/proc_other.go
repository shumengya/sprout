//go:build !unix && !windows

package platform

import "os/exec"

// ConfigureCmd 在既非 Unix 也非 Windows 的平台上沿用 exec 默认行为（只终止直接子进程）。
func ConfigureCmd(*exec.Cmd) {}
