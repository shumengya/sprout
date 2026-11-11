package session

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/event"
)

// testPNG 返回一张 3×3 png 的 base64。
func testPNG(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 3, 3))
	img.Set(1, 1, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestRecorderStoresImagesNextToSession(t *testing.T) {
	dir := t.TempDir()
	rec := NewRecorder(dir, Info{Provider: "p", Model: "m", Cwd: `D:\proj`}, nil)
	b64 := testPNG(t)
	rec.Emit(event.TurnStarted{Prompt: "看图", Images: []event.ImageInfo{{Name: "a.png", MediaType: "image/png", Data: b64}}})
	rec.Emit(event.MessageCompleted{Content: "看到了"})
	rec.Emit(event.TurnFinished{Reason: event.ReasonStop})
	path := rec.Path()
	rec.Close()

	if path == "" {
		t.Fatal("应创建会话文件")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), b64) {
		t.Error("JSONL 里不应内联 base64 图片数据")
	}
	if !strings.Contains(string(raw), `"images":[{"file":"`) {
		t.Fatalf("user 记录应带图片引用: %s", raw)
	}

	// 图片文件应落在会话同名目录里，内容与输入一致。
	imgDir := strings.TrimSuffix(path, ".jsonl") + ".images"
	entries, err := os.ReadDir(imgDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("图片目录应有 1 个文件: %v %v", entries, err)
	}
	if entries[0].Name()[len(entries[0].Name())-4:] != ".png" {
		t.Errorf("文件名应按类型加扩展名: %s", entries[0].Name())
	}
	got, err := os.ReadFile(filepath.Join(imgDir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if base64.StdEncoding.EncodeToString(got) != b64 {
		t.Error("落盘图片内容应与输入一致")
	}
}

func TestReplayRestoresImagesFromDisk(t *testing.T) {
	dir := t.TempDir()
	b64 := testPNG(t)
	rec := NewRecorder(dir, Info{Provider: "p", Model: "m", Cwd: `D:\proj`}, nil)
	rec.Emit(event.TurnStarted{Prompt: "看图", Images: []event.ImageInfo{{Name: "a.png", MediaType: "image/png", Data: b64}}})
	rec.Emit(event.MessageCompleted{Content: "看到了"})
	rec.Emit(event.TurnFinished{Reason: event.ReasonStop})
	path := rec.Path()
	rec.Close()

	st, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.History) != 2 {
		t.Fatalf("历史条数 = %d: %+v", len(st.History), st.History)
	}
	user := st.History[0]
	if len(user.Content) != 2 || user.Content[0].Type != agent.BlockText || user.Content[1].Type != agent.BlockImage {
		t.Fatalf("恢复出的用户消息应是 文本 + 图片: %+v", user.Content)
	}
	if user.Content[1].Data != b64 {
		t.Error("恢复出的图片数据应与当初完全一致（保证历史逐条相等）")
	}

	// 事件流里应带上图片说明，供 TUI 显示。
	var img []event.ImageInfo
	for _, e := range st.Events {
		if ts, ok := e.(event.TurnStarted); ok {
			img = ts.Images
		}
	}
	if len(img) != 1 || img[0].Name == "" || img[0].Bytes == 0 {
		t.Errorf("TurnStarted 应带图片说明: %+v", img)
	}
}

func TestReplayImageOnlyPromptHasNoEmptyTextBlock(t *testing.T) {
	dir := t.TempDir()
	rec := NewRecorder(dir, Info{Provider: "p", Model: "m", Cwd: `D:\proj`}, nil)
	rec.Emit(event.TurnStarted{Images: []event.ImageInfo{{Name: "a.png", MediaType: "image/png", Data: testPNG(t)}}})
	rec.Emit(event.MessageCompleted{Content: "看到了"})
	rec.Emit(event.TurnFinished{Reason: event.ReasonStop})
	path := rec.Path()
	rec.Close()

	st, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	user := st.History[0]
	if len(user.Content) != 1 || user.Content[0].Type != agent.BlockImage {
		t.Errorf("纯图片提问恢复后不应有空文本块: %+v", user.Content)
	}
}

func TestReplayMissingImageFileKeepsNote(t *testing.T) {
	dir := t.TempDir()
	rec := NewRecorder(dir, Info{Provider: "p", Model: "m", Cwd: `D:\proj`}, nil)
	rec.Emit(event.TurnStarted{Prompt: "看图", Images: []event.ImageInfo{{Name: "a.png", MediaType: "image/png", Data: testPNG(t)}}})
	rec.Emit(event.MessageCompleted{Content: "看到了"})
	rec.Emit(event.TurnFinished{Reason: event.ReasonStop})
	path := rec.Path()
	rec.Close()

	if err := os.RemoveAll(strings.TrimSuffix(path, ".jsonl") + ".images"); err != nil {
		t.Fatal(err)
	}
	st, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	user := st.History[0]
	last := user.Content[len(user.Content)-1]
	if last.Type != agent.BlockText || !strings.Contains(last.Text, "图片无法恢复") {
		t.Errorf("图片丢失时应用文字说明代替: %+v", last)
	}
}

func TestListMarksSessionsWithImages(t *testing.T) {
	dir := t.TempDir()
	rec := NewRecorder(dir, Info{Provider: "p", Model: "m", Cwd: `D:\proj`}, nil)
	rec.Emit(event.TurnStarted{Images: []event.ImageInfo{{Name: "a.png", MediaType: "image/png", Data: testPNG(t)}}})
	rec.Emit(event.MessageCompleted{Content: "看到了"})
	rec.Emit(event.TurnFinished{Reason: event.ReasonStop})
	rec.Close()

	list, err := List(dir, `D:\proj`)
	if err != nil || len(list) != 1 {
		t.Fatalf("%v %+v", err, list)
	}
	if !strings.Contains(list[0].Prompt, "[图片]") {
		t.Errorf("列表摘要应标出图片: %q", list[0].Prompt)
	}
}

func TestUserRecordImageJSONShape(t *testing.T) {
	b, err := json.Marshal(userRec{
		header: header{Type: "user"}, Content: "x",
		Images: []imageRec{{File: "a.images/x.png", MediaType: "image/png", Bytes: 12}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"images":[{"file":"a.images/x.png","media_type":"image/png","bytes":12}]`) {
		t.Errorf("记录格式变了，会影响旧会话兼容: %s", b)
	}
}
