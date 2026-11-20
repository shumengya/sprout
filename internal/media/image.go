// Package media 读取用户提供的图片（文件或剪贴板），转成内核的 agent.Image。
//
// 它只做「读入 + 认类型 + 必要时缩放压缩」，不关心命令行还是 TUI 调用；
// 也负责把大小格式化成给人看的文本。
package media

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/shumengya/sprout/internal/agent"
)

// MaxImageBytes 是发给模型的单张图片上限。多数服务商限制 base64 后 5MB 左右，
// 乘 3/4 得到原始字节上限，留一点余量。超过的图片先缩放、重新编码，压不下来才拒绝。
const MaxImageBytes = 3 << 20

// MaxImageEdge 是发给模型的图片长边上限（像素）。服务商收到更大的图也会在服务端缩小，
// 白白多传几 MB；Anthropic 在一次请求里图片较多时还会直接拒绝长边超过 2000 的图。
const MaxImageEdge = 2000

// 读入原图的上限：大小和像素数。再大多半不是截图或照片，完整解码会占用几百 MB 内存。
const (
	maxSourceBytes  = 64 << 20
	maxSourcePixels = 80_000_000
)

// LoadImage 读取一张图片，返回可直接交给内核的 Image。name 是展示用的文件名。
func LoadImage(path string) (img agent.Image, name string, err error) {
	name = filepath.Base(path)
	f, err := os.Open(path)
	if err != nil {
		return agent.Image{}, "", fmt.Errorf("读取图片失败: %w", err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxSourceBytes+1))
	if err != nil {
		return agent.Image{}, "", fmt.Errorf("读取图片失败: %w", err)
	}
	img, err = Prepare(raw, name)
	if err != nil {
		return agent.Image{}, "", err
	}
	return img, name, nil
}

// Prepare 把原始图片字节整理成可以发给模型的图片：按内容认类型；
// 大小和尺寸都在上限内时原样发送（不重新编码，画质、动图都不变），
// 否则缩到长边不超过 MaxImageEdge 并重新编码，直到不超过 MaxImageBytes。
func Prepare(raw []byte, name string) (agent.Image, error) {
	switch {
	case len(raw) == 0:
		return agent.Image{}, fmt.Errorf("图片 %s 是空文件", name)
	case len(raw) > maxSourceBytes:
		return agent.Image{}, fmt.Errorf("图片 %s 超过 %s，不处理", name, Size(maxSourceBytes))
	}
	mt := sniffType(raw)
	if mt == "" {
		return agent.Image{}, fmt.Errorf("%s 不是支持的图片格式（支持 png / jpeg / gif / webp）", name)
	}
	out, outType, err := fit(raw, mt)
	if err != nil {
		return agent.Image{}, fmt.Errorf("图片 %s %v", name, err)
	}
	return agent.Image{MediaType: outType, Data: base64.StdEncoding.EncodeToString(out), Name: name}, nil
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
