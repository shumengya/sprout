package platform

import (
	"fmt"
	"os"
	"path/filepath"
)

// ReplaceExecutable 用 newExe 的内容替换 exePath 指向的程序。
//
// newExe 必须和 exePath 在同一个文件系统上（调用方先写到同目录的临时文件），
// 这样替换是一次 rename，不会出现“写了一半”的程序。
// 正在运行的程序在 Windows 上不能覆盖，只能先改名；Unix 允许直接 rename 覆盖。
func ReplaceExecutable(exePath, newExe string) error {
	return replaceExecutable(exePath, newExe)
}

// RemoveOldExecutable 清理 Windows 上次自更新挪开的旧程序（exePath + ".old"）；其他平台什么都不做。
func RemoveOldExecutable(exePath string) { removeOldExecutable(exePath) }

// ResolveExecutable 返回当前程序在磁盘上的真实路径（解开符号链接），
// 替换时要改的是真实文件，不是指向它的链接。
func ResolveExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("无法定位当前程序: %w", err)
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return exe, nil
}

// CheckWritable 确认能在 exePath 所在目录里创建文件。装到 Program Files 之类
// 需要管理员权限的目录时，这一步会失败，应该在下载之前就报出来。
func CheckWritable(exePath string) error {
	dir := filepath.Dir(exePath)
	f, err := os.CreateTemp(dir, ".sprout-write-test-*")
	if err != nil {
		return fmt.Errorf("程序目录 %s 不可写，请用管理员权限运行（或把 sprout 装到用户目录）: %w", dir, err)
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return nil
}
