//go:build darwin

package platform

import (
	"context"
	"encoding/hex"
	"os/exec"
	"strings"
	"time"
)

// clipboardImage 用 osascript 读剪贴板：先按 PNG 取（系统截图、浏览器复制的图片都能转成 PNG），
// 取不到再看是不是 Finder 里复制的文件。AppleScript 把二进制数据打印成 «data PNGf89504E47…»。
func clipboardImage() ([]byte, []string, error) {
	if out, err := osascript("the clipboard as «class PNGf»"); err == nil {
		s := strings.TrimSpace(out)
		if hexData, ok := strings.CutPrefix(s, "«data PNGf"); ok {
			if b, err := hex.DecodeString(strings.TrimSuffix(hexData, "»")); err == nil && len(b) > 0 {
				return b, nil, nil
			}
		}
	}
	if out, err := osascript("POSIX path of (the clipboard as «class furl»)"); err == nil {
		if p := strings.TrimSpace(out); p != "" {
			return nil, []string{p}, nil
		}
	}
	// 两种都转换失败说明剪贴板里没有图片（osascript 对“不能转换”只报错，不区分原因）。
	return nil, nil, nil
}

func osascript(script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "osascript", "-e", script).Output()
	return string(out), err
}
