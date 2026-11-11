// Package media 读取用户提供的图片文件，转成内核的 agent.Image。
//
// 它只做「读文件 + 认类型 + 限大小」，不关心命令行还是 TUI 调用；
// 也负责把大小格式化成给人看的文本。
package media

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/shumengya/sprout/internal/agent"
)

// MaxImageBytes 是单张图片的大小上限。多数服务商限制 base64 后 5MB 左右，
// 乘 3/4 得到原始字节上限，留一点余量。
const MaxImageBytes = 3 << 20

// LoadImage 读取一张图片，返回可直接交给内核的 Image。name 是展示用的文件名。
func LoadImage(path string) (img agent.Image, name string, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return agent.Image{}, "", fmt.Errorf("读取图片失败: %w", err)
	}
	if len(raw) == 0 {
		return agent.Image{}, "", fmt.Errorf("图片 %s 是空文件", filepath.Base(path))
	}
	if len(raw) > MaxImageBytes {
		return agent.Image{}, "", fmt.Errorf("图片 %s 有 %s，超过上限 %s", filepath.Base(path), Size(len(raw)), Size(MaxImageBytes))
	}
	mt := sniffType(raw)
	if mt == "" {
		return agent.Image{}, "", fmt.Errorf("%s 不是支持的图片格式（支持 png / jpeg / gif / webp）", filepath.Base(path))
	}
	return agent.Image{MediaType: mt, Data: base64.StdEncoding.EncodeToString(raw), Name: filepath.Base(path)}, filepath.Base(path), nil
}

// sniffType 用内容判断类型，扩展名只作参考：用户可能把 png 存成 .jpg。
func sniffType(raw []byte) string {
	mt := http.DetectContentType(raw)
	switch mt {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return mt
	}
	return ""
}

// Size 把字节数格式化成 KB / MB 的短文本。
func Size(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// LooksLikeImagePath 判断一段文本是否像一个图片文件路径（用于拖拽文件进终端时自动识别）。
func LooksLikeImagePath(s string) bool {
	s = strings.Trim(strings.TrimSpace(s), `"'`)
	if strings.ContainsAny(s, "\r\n") || s == "" {
		return false
	}
	if agent.MediaTypeByExt(s) == "" {
		return false
	}
	st, err := os.Stat(s)
	return err == nil && !st.IsDir()
}
