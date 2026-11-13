package file

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeAtomic 先写同目录下的临时文件再原子替换 abs，中途失败不会把原文件变成半截。
// mode 是目标权限；覆盖已有文件时调用方应传入原权限。
func writeAtomic(abs, content string, mode os.FileMode) error {
	dir := filepath.Dir(abs)
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建目录 %s 失败: %w", dir, err)
		}
	}
	tmp, err := os.CreateTemp(dir, ".sprout-write-*")
	if err != nil {
		return fmt.Errorf("在 %s 下创建临时文件失败: %w", dir, err)
	}
	tmpName := tmp.Name()
	cleanup := func() { os.Remove(tmpName) }

	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("写入 %s 失败: %w", abs, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}
	// CreateTemp 建的是 0600；普通文本文件给 0644，覆盖已有文件时用原权限。
	if err := os.Chmod(tmpName, mode); err != nil {
		cleanup()
		return fmt.Errorf("设置文件权限失败: %w", err)
	}
	if err := os.Rename(tmpName, abs); err != nil {
		cleanup()
		return fmt.Errorf("替换 %s 失败: %w", abs, err)
	}
	return nil
}

// fileMode 返回覆盖 abs 时应沿用的权限：文件不存在时给 0644。
func fileMode(abs string) os.FileMode {
	if info, err := os.Stat(abs); err == nil {
		return info.Mode().Perm()
	}
	return 0o644
}
