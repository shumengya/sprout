// Package file 实现 read / write 两个文件工具。
//
// 它们是 bash / powershell 之外的快捷方式：读写单个文件不必起进程，也能避免引号与转义的坑。
// 路径语义与 shell 工具一致：相对路径相对启动时的工作目录，不是进程当前目录。
package file

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MaxReadBytes 是 read 允许读取的单个文件上限。超过时提示用 offset/limit 或改用 shell 工具。
const MaxReadBytes = 10 << 20

// ErrIsDir 表示目标是一个目录。
var ErrIsDir = errors.New("目标是一个目录，不是文件")

// resolve 把工具参数里的路径解析成绝对路径。相对路径相对 dir（agent 的工作目录）。
//
// 不展开 ~：Windows 与 Unix 的 home 语义不同，交给模型写绝对路径或让 shell 处理更可预测。
func resolve(dir, p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("参数 path 不能为空")
	}
	if strings.HasPrefix(p, "~") {
		return "", errors.New("不支持 ~ 开头的路径，请给出绝对路径，或使用 bash / powershell 工具处理")
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	return filepath.Clean(p), nil
}

// checkFile 确认路径存在且是普通文件，返回文件信息。
func checkFile(abs string) (os.FileInfo, error) {
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("无法访问 %s: %w", abs, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s: %w", abs, ErrIsDir)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s 不是普通文件（%s），请使用 shell 工具处理", abs, info.Mode().Type())
	}
	return info, nil
}

// isBinary 用前 8KB 里是否出现 NUL 字节判断二进制文件。
// 文本文件（含 UTF-8 中文、UTF-16 之外的大多数编码）不会出现 NUL。
func isBinary(b []byte) bool {
	if len(b) > 8192 {
		b = b[:8192]
	}
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}
