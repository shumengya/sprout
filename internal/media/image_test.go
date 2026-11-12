package media

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
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

func TestLoadImageRejectsOversize(t *testing.T) {
	dir := t.TempDir()
	p := writePNG(t, dir, "big.png", 2, 2)
	raw, _ := os.ReadFile(p)
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(make([]byte, MaxImageBytes))
	f.Close()
	if _, _, err := LoadImage(p); err == nil || !strings.Contains(err.Error(), "超过上限") {
		t.Errorf("超限应被拒绝: %v（原大小 %d）", err, len(raw))
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
	dir := t.TempDir()
	big := writePNG(t, dir, "big.png", 1, 1)
	f, _ := os.OpenFile(big, os.O_WRONLY|os.O_APPEND, 0o600)
	f.Write(make([]byte, MaxImageBytes))
	f.Close()

	in, attached, skipped := AutoAttach(agent.Input{Text: "看 " + big})
	if len(attached) != 0 || len(skipped) != 1 || len(in.Images) != 0 {
		t.Fatalf("超限图片应跳过并说明: attached=%+v skipped=%v", attached, skipped)
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
