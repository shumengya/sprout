package agent

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// pngB64 生成一张 w×h 的纯色 png 并返回 base64，用于图片相关的测试。
func pngB64(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{R: 200, G: 30, B: 60, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestInputMessageKeepsTextThenImages(t *testing.T) {
	b64 := pngB64(t, 4, 4)
	m := Input{Text: "看看这个", Images: []Image{
		{MediaType: "image/png", Data: b64},
		{MediaType: "image/jpeg", Data: "AAAA"},
	}}.Message()
	if m.Role != RoleUser || len(m.Content) != 3 {
		t.Fatalf("内容 = %+v", m.Content)
	}
	if m.Content[0].Type != BlockText || m.Content[0].Text != "看看这个" {
		t.Errorf("第一块应是文本: %+v", m.Content[0])
	}
	if m.Content[1].Type != BlockImage || m.Content[1].MediaType != "image/png" || m.Content[1].Data != b64 {
		t.Errorf("第二块应是 png 图片: %+v", m.Content[1])
	}
	if m.Content[2].MediaType != "image/jpeg" {
		t.Errorf("第三块应是 jpeg 图片: %+v", m.Content[2])
	}
	if !m.HasImages() {
		t.Error("HasImages 应为 true")
	}
}

func TestInputMessageOmitsEmptyTextWhenImageOnly(t *testing.T) {
	m := Input{Images: []Image{{MediaType: "image/png", Data: "AAAA"}}}.Message()
	if len(m.Content) != 1 || m.Content[0].Type != BlockImage {
		t.Fatalf("纯图片提问不应带空文本块: %+v", m.Content)
	}
}

func TestWithoutImagesReplacesRunsOnce(t *testing.T) {
	m := Message{Role: RoleUser, Content: []Block{
		TextBlock("前"), ImageBlock("image/png", "AA"), ImageBlock("image/png", "BB"),
		TextBlock("中"), ImageBlock("image/png", "CC"),
	}}
	got := m.WithoutImages("（图片已省略）")
	want := []Block{TextBlock("前"), TextBlock("（图片已省略）"), TextBlock("中"), TextBlock("（图片已省略）")}
	if len(got.Content) != len(want) {
		t.Fatalf("块数 = %d, 期望 %d: %+v", len(got.Content), len(want), got.Content)
	}
	for i := range want {
		if got.Content[i] != want[i] {
			t.Errorf("第 %d 块 = %+v, 期望 %+v", i, got.Content[i], want[i])
		}
	}
	if m.HasImages() != true || got.HasImages() {
		t.Error("WithoutImages 应返回去掉图片的副本，且不改原消息")
	}
}

func TestWithoutImagesNoopWhenNoImage(t *testing.T) {
	m := NewTextMessage(RoleUser, "hi")
	got := m.WithoutImages("x")
	if len(got.Content) != 1 || got.Content[0].Text != "hi" {
		t.Errorf("没有图片时应原样返回: %+v", got.Content)
	}
}

func TestEstimateImageTokens(t *testing.T) {
	small := EstimateImageTokens("image/png", pngB64(t, 10, 10))
	if small != imageTokenMin {
		t.Errorf("小图应取下限 %d，得到 %d", imageTokenMin, small)
	}
	// 1000×1000 = 1e6 / 750 ≈ 1333，落在上下限之间。
	mid := EstimateImageTokens("image/png", pngB64(t, 1000, 1000))
	if mid < 1300 || mid > 1400 {
		t.Errorf("1000×1000 应约 1333 token，得到 %d", mid)
	}
	// 4000×4000 = 1.6e7 / 750 ≈ 21333，应被上限截断。
	big := EstimateImageTokens("image/png", pngB64(t, 4000, 4000))
	if big != imageTokenMax {
		t.Errorf("大图应取上限 %d，得到 %d", imageTokenMax, big)
	}
	if got := EstimateImageTokens("image/webp", "不是 base64"); got != imageTokenFallb {
		t.Errorf("解不出时应回退 %d，得到 %d", imageTokenFallb, got)
	}
}

func TestEstimateMessageCountsImages(t *testing.T) {
	text := estimateMessage(NewTextMessage(RoleUser, "看"))
	withImg := estimateMessage(Message{Role: RoleUser, Content: []Block{
		TextBlock("看"), ImageBlock("image/png", pngB64(t, 1000, 1000)),
	}})
	if withImg-text < 1300 {
		t.Errorf("图片应计入 token: 纯文本 %d，带图 %d", text, withImg)
	}
}

func TestMediaTypeHelpers(t *testing.T) {
	cases := map[string]string{
		"a.png": "image/png", "A.JPEG": "image/jpeg", "b.jpg": "image/jpeg",
		"c.gif": "image/gif", "d.webp": "image/webp", "e.txt": "", "noext": "",
	}
	for name, want := range cases {
		if got := MediaTypeByExt(name); got != want {
			t.Errorf("MediaTypeByExt(%q) = %q, 期望 %q", name, got, want)
		}
	}
	if ImageExt("image/jpeg") != ".jpg" || ImageExt("application/pdf") != ".bin" {
		t.Error("ImageExt 映射不对")
	}
}
