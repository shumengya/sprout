package tui

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/event"
)

// tempPNG 在临时目录写一张小 png，返回路径。
func tempPNG(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "截图.png")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestImageCommandAttachesAndSends(t *testing.T) {
	got := make(chan agent.Input, 1)
	r := fakeRunner{run: func(_ context.Context, in agent.Input, sink event.Sink) error {
		got <- in
		sink.Emit(event.TurnFinished{Reason: event.ReasonStop})
		return nil
	}}
	m := newSized(t, Options{Runner: r, Info: Info{Model: "m", Vision: true}}, 120, 40)

	submit(m, "/image "+tempPNG(t))
	mustContain(t, plain(m), "已附加图片", "截图.png")
	if len(m.pending) != 1 {
		t.Fatalf("待发送图片 = %d", len(m.pending))
	}

	// 必须走真实的 Bubble Tea 事件循环：startTurn 返回的是 tea.Batch，只有 Program 会执行里面的 goroutine。
	m.in.SetValue("这张图里是什么")
	runProgram(t, m, func(send func(tea.Msg)) { send(keyEnter) }, finishedOneTurn)

	var in agent.Input
	select {
	case in = <-got:
	default:
		t.Fatal("runner 没有被调用")
	}
	if in.Text != "这张图里是什么" || len(in.Images) != 1 || in.Images[0].MediaType != "image/png" {
		t.Fatalf("提交给内核的输入不对: %+v", in)
	}
	if raw, err := base64.StdEncoding.DecodeString(in.Images[0].Data); err != nil || len(raw) == 0 {
		t.Error("图片数据应是合法 base64")
	}
	if len(m.pending) != 0 {
		t.Error("发送后应清空待发送图片")
	}
	mustContain(t, plain(m), "[图片] 截图.png")
}

func TestImageCommandListAndClear(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner(), Info: Info{Model: "m", Vision: true}}, 120, 40)
	submit(m, "/image")
	mustContain(t, plain(m), "没有待发送的图片")

	submit(m, "/image "+tempPNG(t))
	submit(m, "/image")
	mustContain(t, plain(m), "待发送的图片（1 张）")

	submit(m, "/image clear")
	mustContain(t, plain(m), "已清空 1 张")
	if len(m.pending) != 0 {
		t.Error("clear 后应清空")
	}
}

func TestImageCommandRejectsBadFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "not-image.txt")
	if err := os.WriteFile(p, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newSized(t, Options{Runner: scriptRunner()}, 120, 40)
	submit(m, "/image "+p)
	mustContain(t, plain(m), "不是支持的图片格式")
	if len(m.pending) != 0 {
		t.Error("失败时不应附加")
	}
}

func TestImageWarnsWhenModelLacksVision(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 120, 40) // testInfo 没有 Vision
	submit(m, "/image "+tempPNG(t))
	mustContain(t, plain(m), "没有标注 vision")
}

func TestReplayShowsImageMarks(t *testing.T) {
	events := []event.Event{
		event.TurnStarted{Prompt: "看图", Images: []event.ImageInfo{{Name: "a.png", MediaType: "image/png", Bytes: 2048}}},
		event.MessageCompleted{Content: "看到了"},
		event.TurnFinished{Reason: event.ReasonStop},
	}
	m := newSized(t, Options{Runner: scriptRunner(), Replay: events}, 120, 40)
	mustContain(t, plain(m), "[图片] a.png", "2 KB")
}

func TestImageHelpMentionsImage(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 80)
	submit(m, "/help")
	mustContain(t, plain(m), "/image")
}
