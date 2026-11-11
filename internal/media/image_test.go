package media

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestSizeAndPathDetection(t *testing.T) {
	if Size(512) != "512 B" || Size(2048) != "2 KB" || Size(2<<20) != "2.0 MB" {
		t.Errorf("Size 格式化不对: %s %s %s", Size(512), Size(2048), Size(2<<20))
	}
	p := writePNG(t, t.TempDir(), "a.png", 1, 1)
	if !LooksLikeImagePath(`"` + p + `"`) {
		t.Error("带引号的图片路径应识别")
	}
	if LooksLikeImagePath("a.png") || LooksLikeImagePath(p+"\n第二行") {
		t.Error("不存在的路径或多行文本不应识别")
	}
}
