package media

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent"
)

func writePNG(t *testing.T, dir, name string, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadImageSniffsContentNotExtension(t *testing.T) {
	// 内容是 png，扩展名却写 jpg：应按内容识别。
	p := writePNG(t, t.TempDir(), "伪装的.jpg", 2, 2)
	img, name, err := LoadImage(p)
	if err != nil {
		t.Fatal(err)
	}
	if img.MediaType != "image/png" {
		t.Errorf("mediaType = %q", img.MediaType)
	}
	if name != "伪装的.jpg" || img.Name != name {
		t.Errorf("name = %q / %q", name, img.Name)
	}
	raw, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil || len(raw) == 0 {
		t.Error("Data 应是合法 base64")
	}
}

func TestLoadImageRejectsNonImageAndEmpty(t *testing.T) {
	dir := t.TempDir()
	txt := filepath.Join(dir, "a.txt")
	os.WriteFile(txt, []byte("hello"), 0o600)
	if _, _, err := LoadImage(txt); err == nil || !strings.Contains(err.Error(), "不是支持的图片格式") {
		t.Errorf("文本文件应被拒绝: %v", err)
	}

	empty := filepath.Join(dir, "b.png")
	os.WriteFile(empty, nil, 0o600)
	if _, _, err := LoadImage(empty); err == nil || !strings.Contains(err.Error(), "空文件") {
		t.Errorf("空文件应被拒绝: %v", err)
	}

	if _, _, err := LoadImage(filepath.Join(dir, "没有.png")); err == nil {
		t.Error("不存在的文件应报错")
	}
}

// 超过大小上限、但能解码的图片压缩后发送，而不是拒绝。
func TestLoadImageShrinksOversize(t *testing.T) {
	p := filepath.Join(t.TempDir(), "noise.png")
	// 随机噪点几乎压不动：1100×1100 的 png 约 4.6 MB，长边却在上限内，只能靠换成 jpeg 压下来。
	writeFile(t, p, encodePNGBytes(t, noise(1100, 1100)))
	if st, _ := os.Stat(p); st.Size() <= MaxImageBytes {
		t.Fatalf("测试前提不成立：原图 %d 字节没有超限", st.Size())
	}
	img, name, err := LoadImage(p)
	if err != nil {
		t.Fatal(err)
	}
	raw := decoded(t, img)
	if len(raw) > MaxImageBytes || img.MediaType != "image/jpeg" || name != "noise.png" {
		t.Fatalf("应压成不超过上限的 jpeg 并保留原名：%s %d 字节，name=%q", img.MediaType, len(raw), name)
	}
}

// 长边超过上限的图等比缩小；截图类（png）仍用 png。
func TestLoadImageScalesDownLongEdge(t *testing.T) {
	p := writePNG(t, t.TempDir(), "wide.png", 3000, 600)
	img, _, err := LoadImage(p)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config(t, img)
	if img.MediaType != "image/png" || cfg.Width != MaxImageEdge || cfg.Height != 400 {
		t.Fatalf("应缩到 %d×400 的 png，得到 %s %d×%d", MaxImageEdge, img.MediaType, cfg.Width, cfg.Height)
	}
}

// 小图原样发送，不重新编码。
func TestLoadImageKeepsSmallImageBytes(t *testing.T) {
	p := writePNG(t, t.TempDir(), "small.png", 40, 30)
	want, _ := os.ReadFile(p)
	img, _, err := LoadImage(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded(t, img), want) {
		t.Error("没超限的图片应原样发送")
	}
}

// 手机照片靠 EXIF 标记方向；重新编码会丢掉 EXIF，所以缩放时要把像素转正。
func TestLoadImageAppliesExifOrientation(t *testing.T) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2400, 100)), nil); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "photo.jpg")
	writeFile(t, p, withOrientation(buf.Bytes(), 6)) // 6：显示时要顺时针转 90°
	img, _, err := LoadImage(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg := config(t, img); cfg.Width != 83 || cfg.Height != MaxImageEdge {
		t.Fatalf("应转正并缩到 83×%d，得到 %d×%d", MaxImageEdge, cfg.Width, cfg.Height)
	}
}

func TestExifOrientationParsing(t *testing.T) {
	var buf bytes.Buffer
	jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil)
	for _, o := range []int{1, 3, 6, 8} {
		if got := jpegOrientation(withOrientation(buf.Bytes(), o)); got != o {
			t.Errorf("方向 %d 读成了 %d", o, got)
		}
	}
	if got := jpegOrientation(buf.Bytes()); got != 1 {
		t.Errorf("没有 EXIF 时应为 1，得到 %d", got)
	}
}

// orientImage 的八种方向：用 2×1 的两色图，看左上角落到哪里。
func TestOrientImage(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2, 1))
	red := color.RGBA{R: 255, A: 255}
	src.Set(0, 0, red)
	cases := map[int]struct{ w, h, rx, ry int }{
		1: {2, 1, 0, 0}, 2: {2, 1, 1, 0}, 3: {2, 1, 1, 0}, 4: {2, 1, 0, 0},
		5: {1, 2, 0, 0}, 6: {1, 2, 0, 0}, 7: {1, 2, 0, 1}, 8: {1, 2, 0, 1},
	}
	for o, c := range cases {
		dst := orientImage(src, o)
		if dst.Bounds().Dx() != c.w || dst.Bounds().Dy() != c.h || dst.RGBAAt(c.rx, c.ry) != red {
			t.Errorf("方向 %d：尺寸 %v，红点应在 (%d,%d)", o, dst.Bounds(), c.rx, c.ry)
		}
	}
}

func TestSizeFormatting(t *testing.T) {
	if Size(512) != "512 B" || Size(2048) != "2 KB" || Size(2<<20) != "2.0 MB" {
		t.Errorf("Size 格式化不对: %s %s %s", Size(512), Size(2048), Size(2<<20))
	}
}

func TestFindImagePaths(t *testing.T) {
	dir := t.TempDir()
	a := writePNG(t, dir, "a.png", 1, 1)
	spaced := writePNG(t, dir, "含 空格 的.png", 1, 1)
	txt := filepath.Join(dir, "notes.txt")
	os.WriteFile(txt, []byte("x"), 0o600)

	text := `看这两张：` + a + `。还有"` + spaced + `" 以及 ` + txt + ` 和 ` + filepath.Join(dir, "不存在.png")
	got := FindImagePaths(text)
	if len(got) != 2 || got[0] != a || got[1] != spaced {
		t.Fatalf("应找到两张存在的图片: %q", got)
	}

	if got := FindImagePaths(a + " " + a); len(got) != 1 {
		t.Errorf("同一路径应去重: %q", got)
	}
	if got := FindImagePaths("没有图片路径的一段话"); len(got) != 0 {
		t.Errorf("不应误报: %q", got)
	}
}

// 中文直接贴着路径写时，路径起点会落在分隔符上。Unix 的绝对路径以 / 开头，
// 起点必须保留这个斜杠；早期版本从斜杠前一个字符截起，得到 tmp/x.png 这种相对路径，
// stat 失败，于是 Windows 上正常、Linux 上找不到图。
func TestFindImagePathsGluedToChinese(t *testing.T) {
	dir := t.TempDir()
	p := writePNG(t, dir, "截图.png", 1, 1)
	text := "看这张图：" + p + "这张图的内容"
	got := FindImagePaths(text)
	if len(got) != 1 || got[0] != p {
		t.Fatalf("路径应该被找到: %q（文本 %q）", got, text)
	}
}

func TestAutoAttachReadsPathsFromTextAndDedupes(t *testing.T) {
	dir := t.TempDir()
	a := writePNG(t, dir, "a.png", 1, 1)
	b := writePNG(t, dir, "b.png", 2, 2) // 尺寸不同→内容不同，免得被按内容去重

	in, attached, skipped := AutoAttach(agent.Input{Text: "对比 " + a + " 和 " + b})
	if len(attached) != 2 || len(skipped) != 0 || len(in.Images) != 2 {
		t.Fatalf("attached=%+v skipped=%v images=%d", attached, skipped, len(in.Images))
	}
	if in.Images[0].MediaType != "image/png" || in.Images[0].Name != "a.png" {
		t.Errorf("图片信息不对: %+v", in.Images[0])
	}

	// 已经显式附加过的同一张图不重复读。
	pre, _, _ := AutoAttach(agent.Input{Text: a})
	in2, attached2, _ := AutoAttach(agent.Input{Text: "再看 " + a, Images: pre.Images})
	if len(attached2) != 0 || len(in2.Images) != 1 {
		t.Errorf("重复图片不应再加: attached=%+v images=%d", attached2, len(in2.Images))
	}
}

func TestAutoAttachSkipsBadFilesWithReason(t *testing.T) {
	// 文件头是 png、内容却是坏的，又超过大小上限：既不能原样发，也没法解码来压缩。
	big := filepath.Join(t.TempDir(), "big.png")
	writeFile(t, big, append([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, make([]byte, MaxImageBytes)...))

	in, attached, skipped := AutoAttach(agent.Input{Text: "看 " + big})
	if len(attached) != 0 || len(skipped) != 1 || len(in.Images) != 0 {
		t.Fatalf("坏图片应跳过并说明: attached=%+v skipped=%v", attached, skipped)
	}
	if !strings.Contains(skipped[0], "超过上限") {
		t.Errorf("跳过原因应可读: %q", skipped[0])
	}
	// 文本里的路径保留，用户不会因为附加失败而丢失信息。
	if !strings.Contains(in.Text, big) {
		t.Error("原始文本不应被改动")
	}
}

func TestAutoAttachCapsImageCount(t *testing.T) {
	dir := t.TempDir()
	var parts []string
	for i := 0; i < MaxAutoImages+2; i++ {
		parts = append(parts, writePNG(t, dir, fmt.Sprintf("p%d.png", i), i+1, 3)) // 每张内容都不同
	}
	in, attached, skipped := AutoAttach(agent.Input{Text: strings.Join(parts, " ")})
	if len(attached) != MaxAutoImages || len(in.Images) != MaxAutoImages {
		t.Errorf("应只附加 %d 张: %d", MaxAutoImages, len(attached))
	}
	if len(skipped) != 2 {
		t.Errorf("超出的应给出说明: %v", skipped)
	}
}

func writeFile(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func encodePNGBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func noise(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewPCG(1, 2))
	for i := range img.Pix {
		img.Pix[i] = byte(r.Uint32())
	}
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	return img
}

func decoded(t *testing.T, img agent.Image) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func config(t *testing.T, img agent.Image) image.Config {
	t.Helper()
	cfg, _, err := image.DecodeConfig(bytes.NewReader(decoded(t, img)))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// withOrientation 在 jpeg 的 SOI 之后插入一个只含方向标记的 EXIF（APP1）段。
func withOrientation(j []byte, o int) []byte {
	tiff := []byte{0x4D, 0x4D, 0x00, 0x2A, 0x00, 0x00, 0x00, 0x08} // MM：大端，IFD0 在偏移 8
	tiff = binary.BigEndian.AppendUint16(tiff, 1)                  // 1 个条目
	tiff = binary.BigEndian.AppendUint16(tiff, 0x0112)             // Orientation
	tiff = binary.BigEndian.AppendUint16(tiff, 3)                  // SHORT
	tiff = binary.BigEndian.AppendUint32(tiff, 1)                  // 数量 1
	tiff = binary.BigEndian.AppendUint16(tiff, uint16(o))
	tiff = append(tiff, 0, 0, 0, 0, 0, 0)                              // 值字段补齐 + 下一个 IFD 偏移 0
	seg := append([]byte{0x45, 0x78, 0x69, 0x66, 0x00, 0x00}, tiff...) // Exif 加两个 0
	app1 := []byte{0xFF, 0xE1}
	app1 = binary.BigEndian.AppendUint16(app1, uint16(len(seg)+2))
	app1 = append(app1, seg...)
	return append(append(append([]byte{}, j[:2]...), app1...), j[2:]...)
}
