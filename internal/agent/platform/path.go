package platform

import (
	"path/filepath"
	"runtime"
	"strings"
)

// SamePath 报告两个路径是否指向同一位置（按绝对路径比较，不解析符号链接）。
// Windows 和 macOS 的默认文件系统不区分大小写，按不区分大小写比较。
func SamePath(a, b string) bool {
	pa, err1 := filepath.Abs(a)
	pb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return a == b
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(pa, pb)
	}
	return pa == pb
}
