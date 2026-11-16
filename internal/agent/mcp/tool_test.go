package mcp

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"strings"
	"testing"
)

func pngBase64(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestRenderContentAttachesValidImages(t *testing.T) {
	good := pngBase64(t)
	text, imgs := renderContent([]Content{
		{Type: "text", Text: "截图："},
		// 声明的类型写错了：以内容嗅探为准。
		{Type: "image", MimeType: "image/jpeg", Data: good},
		{Type: "image", MimeType: "image/png", Data: "不是 base64"},
		{Type: "image", MimeType: "image/png", Data: base64.StdEncoding.EncodeToString([]byte("plain text"))},
		{Type: "image", MimeType: "image/png", Data: base64.StdEncoding.EncodeToString(make([]byte, maxImageBytes+1))},
	})
	if len(imgs) != 1 || imgs[0].MediaType != "image/png" || imgs[0].Data != good {
		t.Fatalf("只有合格的那张应附上，类型取嗅探结果: %+v", imgs)
	}
	for _, want := range []string{"截图：", "[图片 1：image/png，已附在结果后]", "base64 无效", "不是支持的格式", "超过上限"} {
		if !strings.Contains(text, want) {
			t.Errorf("文本缺少 %q:\n%s", want, text)
		}
	}
}
