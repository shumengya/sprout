//go:build !windows

package platform

import (
	"fmt"
	"os"
)

// replaceExecutable 直接 rename 覆盖：Unix 允许在程序运行期间替换它的映像，
// 正在跑的进程继续用旧 inode，下次启动就是新版本。
func replaceExecutable(exePath, newExe string) error {
	if err := os.Rename(newExe, exePath); err != nil {
		return fmt.Errorf("替换程序失败（可能需要写权限）: %w", err)
	}
	return nil
}

// removeOldExecutable 只有 Windows 会留下 .old。
func removeOldExecutable(string) {}
