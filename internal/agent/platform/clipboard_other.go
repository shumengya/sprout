//go:build android || !(windows || unix)

package platform

import "errors"

// Termux 的 termux-clipboard-get 只能读文字，其他少见平台没有可用的读取方式。
func clipboardImage() ([]byte, []string, error) {
	return nil, nil, errors.New("当前平台不支持从剪贴板读取图片，请用 /image 路径 附加")
}
