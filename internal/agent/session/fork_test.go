package session

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent/event"
)

// otherPNG 返回一张与 testPNG 内容不同的图，落盘文件名（内容哈希）也就不同。
func otherPNG(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 200, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// forkSource 用真实记录器写一个三轮会话：第 1 轮和第 3 轮各带一张不同的图片，第 2 轮有一次工具调用。
func forkSource(t *testing.T) (root, path string) {
	t.Helper()
	root = t.TempDir()
	rec := NewRecorder(root, testInfo, nil)
	rec.Emit(event.TurnStarted{Prompt: "第一问", Images: []event.ImageInfo{{Name: "a.png", MediaType: "image/png", Data: testPNG(t)}}})
	rec.Emit(event.MessageCompleted{Content: "第一答"})
	rec.Emit(event.TurnFinished{Reason: event.ReasonStop})
	rec.Emit(event.TurnStarted{Prompt: "第二问"})
	rec.Emit(event.MessageCompleted{ToolCalls: []event.ToolCall{{ID: "c1", Name: "bash", Arguments: `{}`}}})
	rec.Emit(event.ToolFinished{ID: "c1", Name: "bash", Command: "ls", Stdout: "x", Content: "x"})
	rec.Emit(event.MessageCompleted{Content: "第二答"})
	rec.Emit(event.TurnFinished{Reason: event.ReasonStop})
	rec.Emit(event.TurnStarted{Prompt: "第三问", Images: []event.ImageInfo{{Name: "b.png", MediaType: "image/png", Data: otherPNG(t)}}})
	rec.Emit(event.MessageCompleted{Content: "第三答"})
	rec.Emit(event.TurnFinished{Reason: event.ReasonStop})
	path = rec.Path()
	rec.Close()
	return root, path
}

func TestForkWholeSessionReplaysTheSame(t *testing.T) {
	root, src := forkSource(t)
	before, _ := os.ReadFile(src)

	dst, err := Fork(root, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if dst == src || filepath.Dir(dst) != filepath.Dir(src) {
		t.Fatalf("分支应是同一分组下的新文件: %s", dst)
	}
	if after, _ := os.ReadFile(src); !bytes.Equal(before, after) {
		t.Error("原会话文件不应被改动")
	}
	orig, _ := Load(src)
	got, err := Load(dst)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == orig.ID || got.Turns != 3 || got.Skipped != 0 {
		t.Fatalf("分支应有新 ID 和全部 3 轮: id=%s turns=%d skipped=%d", got.ID, got.Turns, got.Skipped)
	}
	if !reflect.DeepEqual(got.History, orig.History) || got.Info.Model != orig.Info.Model {
		t.Fatalf("分支重放出的历史应与原会话一致:\n%+v\n%+v", got.History, orig.History)
	}
	head := readRecords(t, dst)[0]
	if head["parent"] != orig.ID || head["parent_turns"] != float64(3) {
		t.Errorf("头记录应标明来源: %v", head)
	}
	list, _ := List(root, testInfo.Cwd)
	if len(list) != 2 {
		t.Fatalf("分支应出现在会话列表里: %+v", list)
	}
	if gm := readMeta(filepath.Dir(dst)); gm.Sessions[filepath.Base(dst)].ID != got.ID {
		t.Errorf("分支应登记进 meta.json: %+v", gm.Sessions)
	}
}

func TestForkFirstTurnsCopiesOnlyTheirImages(t *testing.T) {
	root, src := forkSource(t)
	dst, err := Fork(root, src, 1)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Load(dst)
	if err != nil {
		t.Fatal(err)
	}
	if got.Turns != 1 || len(got.History) != 2 || got.History[1].Text() != "第一答" {
		t.Fatalf("只应保留第 1 轮: turns=%d %+v", got.Turns, got.History)
	}
	raw, _ := os.ReadFile(dst)
	if strings.Contains(string(raw), "第二问") || strings.Contains(string(raw), filepath.Base(strings.TrimSuffix(src, ".jsonl"))) {
		t.Fatalf("分支里不应有截断点之后的记录，也不应引用原会话的图片目录:\n%s", raw)
	}
	imgs, err := os.ReadDir(strings.TrimSuffix(dst, ".jsonl") + ".images")
	if err != nil || len(imgs) != 1 {
		t.Fatalf("只应复制第 1 轮的那张图: %v %v", imgs, err)
	}

	// 删掉原会话后分支的图片仍然能恢复：图片是复制过来的，不是引用。
	if err := Delete(root, src, ""); err != nil {
		t.Fatal(err)
	}
	again, err := Load(dst)
	if err != nil {
		t.Fatal(err)
	}
	if c := again.History[0].Content; len(c) != 2 || c[1].Data != testPNG(t) {
		t.Fatalf("原会话删掉后分支的图片应仍可恢复: %+v", c)
	}
}

func TestForkRejectsBadInput(t *testing.T) {
	root, src := forkSource(t)
	if _, err := Fork(root, src, 4); err == nil || !strings.Contains(err.Error(), "只有 3 轮") {
		t.Errorf("轮数超出时应报错: %v", err)
	}
	if _, err := Fork(root, src, -1); err == nil {
		t.Error("负轮数应报错")
	}
	outside := filepath.Join(t.TempDir(), "x.jsonl")
	if err := os.WriteFile(outside, []byte(`{"type":"session","id":"x"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Fork(root, outside, 0); err == nil {
		t.Error("会话目录以外的文件应被拒绝")
	}
	if list, _ := List(root, testInfo.Cwd); len(list) != 1 {
		t.Errorf("失败的分支不应留下文件: %+v", list)
	}
}

// 在压缩之后的某一轮截断：前缀里已有的 compact 记录照常重放，结果与当时运行时的状态一致。
func TestForkAfterCompactionKeepsSummary(t *testing.T) {
	root := t.TempDir()
	rec := NewRecorder(root, testInfo, nil)
	for _, q := range []string{"一", "二"} {
		rec.Emit(event.TurnStarted{Prompt: q})
		rec.Emit(event.MessageCompleted{Content: "答" + q})
		rec.Emit(event.TurnFinished{Reason: event.ReasonStop})
	}
	rec.Emit(event.Compacted{Mode: event.CompactSummary, Dropped: 4, Summary: "前两轮的摘要", Manual: true})
	for _, q := range []string{"三", "四"} {
		rec.Emit(event.TurnStarted{Prompt: q})
		rec.Emit(event.MessageCompleted{Content: "答" + q})
		rec.Emit(event.TurnFinished{Reason: event.ReasonStop})
	}
	src := rec.Path()
	rec.Close()

	dst, err := Fork(root, src, 3)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Load(dst)
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary != "前两轮的摘要" || len(got.History) != 2 || got.History[1].Text() != "答三" {
		t.Fatalf("应保留摘要和第 3 轮: summary=%q %+v", got.Summary, got.History)
	}
	// 截在压缩之前：压缩记录排在第 3 条 user 之前，所以保留 2 轮时它也在前缀里。
	// 这正是当初第 2 轮结束、压缩完成后的状态。
	dst2, err := Fork(root, src, 2)
	if err != nil {
		t.Fatal(err)
	}
	got2, _ := Load(dst2)
	if got2.Summary != "前两轮的摘要" || len(got2.History) != 0 {
		t.Fatalf("保留 2 轮时应是压缩后的状态: summary=%q %+v", got2.Summary, got2.History)
	}
}
