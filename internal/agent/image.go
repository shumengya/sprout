package agent

import (
	"bytes"
	"encoding/base64"
	"image"
	_ "image/gif"  // 注册 gif 解码器，用于读取尺寸
	_ "image/jpeg" // 注册 jpeg 解码器
	_ "image/png"  // 注册 png 解码器
	"strings"
)

// Image 是一次用户输入携带的图片。Data 是 base64 编码的原始图片字节。
// 内核不关心图片来源（文件、剪贴板、拖拽），只要求上层给对 mediaType。
type Image struct {
	Name      string // 展示用的文件名，可选
	MediaType string // 如 image/png
	Data      string // base64，不含 data: 前缀
	// Source 是这条图片在消息文本里对应的原始片段（如用户直接写的路径），
	// 界面可以把它显示成 [文件名]。为空表示图片不在文本里（/image 这类显式附加）。
	// 只用于展示与恢复，不影响发给模型的内容。
	Source string
}

// Input 是 Run 的输入：一段文本加若干图片。
type Input struct {
	Text   string
	Images []Image
}

// Message 把输入转成用户消息。文本在前、图片按原顺序跟在后面，
// 只有图片没有文字时也不额外插空文本块（各协议适配器处理空文本）。
func (in Input) Message() Message {
	m := Message{Role: RoleUser}
	if in.Text != "" || len(in.Images) == 0 {
		m.Content = append(m.Content, TextBlock(in.Text))
	}
	for _, img := range in.Images {
		m.Content = append(m.Content, ImageBlock(img.MediaType, img.Data))
	}
	return m
}

// 图片 token 估算的经验值：按像素面积折算，并夹在上下限之间。
// 各家的真实算法不同（Anthropic 约 面积/750，OpenAI 高档位封顶），这里取一个略偏高的通用近似。
const (
	imageTokenBase  = 750
	imageTokenMin   = 300
	imageTokenMax   = 8000
	imageTokenFallb = 1600 // 解不出尺寸（webp 等）时的保守估计
)

// EstimateImageTokens 估算一张图片占用的 token：能读出尺寸就按面积折算，读不出用固定值。
func EstimateImageTokens(mediaType, b64 string) int {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return imageTokenFallb
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return imageTokenFallb
	}
	n := cfg.Width * cfg.Height / imageTokenBase
	return min(max(n, imageTokenMin), imageTokenMax)
}

// MediaTypeByExt 按文件扩展名推断媒体类型，不支持的格式返回空串。
func MediaTypeByExt(name string) string {
	i := strings.LastIndexByte(name, '.')
	if i < 0 {
		return ""
	}
	switch strings.ToLower(name[i:]) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	}
	return ""
}

// ImageExt 返回媒体类型对应的文件扩展名（含点），未知类型回退 .bin。
func ImageExt(mediaType string) string {
	switch mediaType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	}
	return ".bin"
}
