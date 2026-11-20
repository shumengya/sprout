package tui

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/event"
)

// clipWith 返回一个固定内容的剪贴板替身。
func clipWith(data []byte, paths []string) func() ([]byte, []string, error) {
	return func() ([]byte, []string, error) { return data, paths, nil }
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Alt+V 粘贴剪贴板里的图片：输入框里出现 [剪贴板图片 1]，标记随正文发给模型，图片一起提交。
func TestAltVPastesClipboardImage(t *testing.T) {
	got := make(chan agent.Input, 1)
	r := fakeRunner{run: func(_ context.Context, in agent.Input, sink event.Sink) error {
		got <- in
		sink.Emit(event.TurnFinished{Reason: event.ReasonStop})
		return nil
	}}
	m := newSized(t, Options{Runner: r, Info: Info{Model: "m", Vision: true}, ClipboardImage: clipWith(pngBytes(t), nil)}, 120, 40)
	m.in.SetValue("看看 ")
	feed(m, update(m, press('v', tea.ModAlt)))
	if v := m.in.Value(); v != "看看 [剪贴板图片 1]" {
		t.Fatalf("输入框 = %q", v)
	}
	if len(m.pending) != 1 || m.pending[0].MediaType != "image/png" {
		t.Fatalf("待发送图片 = %+v", m.pending)
	}
	mustContain(t, m.View().Content, "已粘贴剪贴板图片")

	runProgram(t, m, func(send func(tea.Msg)) { send(keyEnter) }, finishedOneTurn)
	in := <-got
	if in.Text != "看看 [剪贴板图片 1]" || len(in.Images) != 1 {
		t.Fatalf("提交给内核的输入不对: %q %d 张", in.Text, len(in.Images))
	}
	mustContain(t, plain(m), "[剪贴板图片 1]")
}

func TestClipboardWithoutImageShowsHint(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner(), ClipboardImage: clipWith(nil, nil)}, 120, 40)
	feed(m, update(m, press('v', tea.ModAlt)))
	if len(m.pending) != 0 || m.in.Value() != "" {
		t.Fatalf("没有图片时不应附加或改动输入框: %+v %q", m.pending, m.in.Value())
	}
	mustContain(t, m.View().Content, "剪贴板里没有图片")
}

// 资源管理器里复制的图片文件按粘贴路径处理，输入框里显示 [文件名]。
func TestCtrlVWithCopiedFileAttachesIt(t *testing.T) {
	p := tempPNG(t)
	m := newSized(t, Options{Runner: scriptRunner(), Info: Info{Model: "m", Vision: true}, ClipboardImage: clipWith(nil, []string{p})}, 120, 40)
	feed(m, update(m, press('v', tea.ModCtrl)))
	if v := m.in.Value(); v != "[截图.png]" || len(m.pending) != 1 || m.pending[0].Source != p {
		t.Fatalf("输入框 = %q，待发送 = %+v", v, m.pending)
	}
}

// 有的终端拦下 Ctrl+V 后，剪贴板里没有文字就送来一次空粘贴：这时去读剪贴板里的图片。
func TestEmptyPasteReadsClipboardImage(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner(), Info: Info{Model: "m", Vision: true}, ClipboardImage: clipWith(pngBytes(t), nil)}, 120, 40)
	feed(m, update(m, tea.PasteMsg{Content: ""}))
	if len(m.pending) != 1 || !strings.Contains(m.in.Value(), "[剪贴板图片 1]") {
		t.Fatalf("空粘贴应读剪贴板图片：输入框 %q，待发送 %d", m.in.Value(), len(m.pending))
	}
}

func TestQuotePaths(t *testing.T) {
	if got := quotePaths([]string{`C:\a b.png`, `D:\c.png`}); got != `"C:\a b.png" D:\c.png` {
		t.Errorf("quotePaths = %q", got)
	}
}
