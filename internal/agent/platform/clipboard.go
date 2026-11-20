package platform

import (
	"bufio"
	"net/url"
	"path/filepath"
	"strings"
)

// ClipboardImage 读取系统剪贴板里的图片：data 是编码好的图片字节（png 等，类型由调用方按内容识别）；
// 剪贴板里是从文件管理器复制的文件时返回 paths（不筛选类型）。两者都为空表示剪贴板里没有图片，
// err 只用于真正的失败（缺少读取工具、读取出错）。各平台的实现见 clipboard_*.go。
func ClipboardImage() (data []byte, paths []string, err error) { return clipboardImage() }

// parseURIList 解析 text/uri-list（RFC 2483）：每行一个 URI，# 开头是注释。只取本地文件。
func parseURIList(s string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		u, err := url.Parse(line)
		if err != nil || u.Scheme != "file" || u.Path == "" {
			continue
		}
		out = append(out, filepath.FromSlash(u.Path))
	}
	return out
}
