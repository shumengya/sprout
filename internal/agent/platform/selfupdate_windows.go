//go:build windows

package platform

import (
	"fmt"
	"os"
)

// replaceExecutable 走 Windows 的改名技巧：正在运行的映像不能覆盖或删除，
// 但允许改名。先把旧程序挪成 .old，再把新文件放到原位置；失败时把旧的挪回去。
// 残留的 .old 在下一次更新时清掉（那时它已经不占用）。
func replaceExecutable(exePath, newExe string) error {
	old := exePath + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exePath, old); err != nil {
		return fmt.Errorf("无法替换正在运行的程序（可能需要管理员权限）: %w", err)
	}
	if err := os.Rename(newExe, exePath); err != nil {
		// 回滚：旧程序原样放回去，用户至少还能继续用现在的版本。
		if rerr := os.Rename(old, exePath); rerr != nil {
			return fmt.Errorf("替换失败（%v），且无法恢复原程序（%v）；原程序在 %s", err, rerr, old)
		}
		return fmt.Errorf("替换程序失败，已恢复原程序: %w", err)
	}
	return nil
}

// removeOldExecutable 删掉上次更新留下的 .old。旧进程还没退出时删不掉，忽略即可，下次再删。
func removeOldExecutable(exePath string) { _ = os.Remove(exePath + ".old") }
