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
	"strings"
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

	submitRun(m, "/image "+tempPNG(t))
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
	// /image 显式附加的图片不在正文里，单独一行显示 [文件名] 和大小。
	mustContain(t, plain(m), "[截图.png]", "90 B")
}

func TestImageCommandListAndClear(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner(), Info: Info{Model: "m", Vision: true}}, 120, 40)
	submit(m, "/image")
	mustContain(t, plain(m), "没有待发送的图片")

	submitRun(m, "/image "+tempPNG(t))
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
	submitRun(m, "/image "+p)
	mustContain(t, plain(m), "不是支持的图片格式")
	if len(m.pending) != 0 {
		t.Error("失败时不应附加")
	}
}

func TestImageWarnsWhenModelLacksVision(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 120, 40) // testInfo 没有 Vision
	submitRun(m, "/image "+tempPNG(t))
	mustContain(t, plain(m), "没有标注 vision")
}

func TestReplayShowsImageMarks(t *testing.T) {
	events := []event.Event{
		event.TurnStarted{Prompt: "看图", Images: []event.ImageInfo{{Name: "a.png", MediaType: "image/png", Bytes: 2048}}},
		event.MessageCompleted{Content: "看到了"},
		event.TurnFinished{Reason: event.ReasonStop},
	}
	m := newSized(t, Options{Runner: scriptRunner(), Replay: events}, 120, 40)
	mustContain(t, plain(m), "[a.png]", "2 KB")
}

func TestImageHelpMentionsImage(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 80)
	submit(m, "/help")
	mustContain(t, plain(m), "/image")
}

// 用户直接把图片路径写进消息里（贴截图路径、拖文件进终端）也要真的发图，
// 而不是把路径当普通文字交给模型，让它自己去 OCR。
func TestImagePathInMessageIsAttachedAutomatically(t *testing.T) {
	got := make(chan agent.Input, 1)
	r := fakeRunner{run: func(_ context.Context, in agent.Input, sink event.Sink) error {
		got <- in
		sink.Emit(event.TurnFinished{Reason: event.ReasonStop})
		return nil
	}}
	m := newSized(t, Options{Runner: r, Info: Info{Model: "m", Vision: true}}, 120, 40)

	path := tempPNG(t)
	m.in.SetValue(path + " 这张图的内容")
	runProgram(t, m, func(send func(tea.Msg)) { send(keyEnter) }, finishedOneTurn)

	var in agent.Input
	select {
	case in = <-got:
	default:
		t.Fatal("runner 没有被调用")
	}
	if len(in.Images) != 1 || in.Images[0].MediaType != "image/png" {
		t.Fatalf("路径应被识别成图片: %+v", in.Images)
	}
	if !strings.Contains(in.Text, path) {
		t.Error("发给模型的原始文本应保留路径")
	}
	// 对话区里路径换成绿色的 [文件名]，不再铺一整条临时目录。
	s := plain(m)
	mustContain(t, s, "[截图.png]")
	if strings.Contains(s, path) {
		t.Errorf("对话区不应再显示完整路径:\n%s", s)
	}
}

// 粘贴图片路径时输入框里也一样显示 [文件名]：直接铺一整条长路径既占地方又难看。
func TestPasteImagePathShowsMarkInInput(t *testing.T) {
	got := make(chan agent.Input, 1)
	r := fakeRunner{run: func(_ context.Context, in agent.Input, sink event.Sink) error {
		got <- in
		sink.Emit(event.TurnFinished{Reason: event.ReasonStop})
		return nil
	}}
	m := newSized(t, Options{Runner: r, Info: Info{Model: "m", Vision: true}}, 120, 40)

	path := tempPNG(t)
	feed(m, update(m, tea.PasteMsg{Content: path + " 这是什么"}))
	if v := m.in.Value(); v != "[截图.png] 这是什么" {
		t.Fatalf("输入框显示 = %q，应把路径换成 [文件名]", v)
	}
	if len(m.pending) != 1 || m.pending[0].Source != path {
		t.Fatalf("粘贴时应附加图片并记下原始路径: %+v", m.pending)
	}
	// 输入框里的标记和对话区一样是绿色的：样式序列只在渲染时加，不进输入框的值。
	if !strings.Contains(m.View().Content, styImg.Render("[截图.png]")) {
		t.Errorf("输入框里的 [文件名] 应显示为绿色:\n%q", m.View().Content)
	}

	runProgram(t, m, func(send func(tea.Msg)) { send(keyEnter) }, finishedOneTurn)
	var in agent.Input
	select {
	case in = <-got:
	default:
		t.Fatal("runner 没有被调用")
	}
	if in.Text != path+" 这是什么" {
		t.Errorf("发给模型的文本应恢复成原始路径，得到 %q", in.Text)
	}
	if len(in.Images) != 1 || in.Images[0].MediaType != "image/png" {
		t.Fatalf("应随消息发送一张图片: %+v", in.Images)
	}
}

// 用户把输入框里的 [文件名] 删掉，说明他不想带这张图了。
func TestDeletingPasteMarkDropsImage(t *testing.T) {
	got := make(chan agent.Input, 1)
	r := fakeRunner{run: func(_ context.Context, in agent.Input, sink event.Sink) error {
		got <- in
		sink.Emit(event.TurnFinished{Reason: event.ReasonStop})
		return nil
	}}
	m := newSized(t, Options{Runner: r, Info: Info{Model: "m", Vision: true}}, 120, 40)

	feed(m, update(m, tea.PasteMsg{Content: tempPNG(t)}))
	m.in.SetValue("只看文字")
	runProgram(t, m, func(send func(tea.Msg)) { send(keyEnter) }, finishedOneTurn)

	select {
	case in := <-got:
		if len(in.Images) != 0 {
			t.Errorf("标记已删除，不应再发图片: %+v", in.Images)
		}
		if in.Text != "只看文字" {
			t.Errorf("文本 = %q", in.Text)
		}
	default:
		t.Fatal("runner 没有被调用")
	}
}

// 普通文本粘贴不受影响。
func TestPastePlainTextUnchanged(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 120, 40)
	m.Update(tea.PasteMsg{Content: "一段普通文字"})
	if v := m.in.Value(); v != "一段普通文字" {
		t.Fatalf("输入框 = %q", v)
	}
	if len(m.pending) != 0 {
		t.Errorf("不应附加图片: %+v", m.pending)
	}
}

func TestAutoAttachNoticeListsSkippedFiles(t *testing.T) {
	// 文件头是 png、内容是坏的，又超过大小上限：没法原样发，也没法解码来压缩。
	big := filepath.Join(t.TempDir(), "坏图.png")
	sig := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	if err := os.WriteFile(big, append(sig, make([]byte, 4<<20)...), 0o600); err != nil {
		t.Fatal(err)
	}

	m := newSized(t, Options{Runner: scriptRunner(), Info: Info{Model: "m", Vision: true}}, 140, 40)
	m.in.SetValue(big)
	runProgram(t, m, func(send func(tea.Msg)) { send(keyEnter) }, finishedOneTurn)
	mustContain(t, plain(m), "有图片没能附加", "超过上限")
}

// submitRun 提交一条输入，并把返回的 Cmd 执行完、结果交回 Update（/image 在后台读图）。
func submitRun(m *Model, text string) {
	m.in.SetValue(text)
	feed(m, update(m, keyEnter))
}

func update(m *Model, msg tea.Msg) tea.Cmd {
	_, cmd := m.Update(msg)
	return cmd
}

// feed 同步执行 cmd，把产生的消息交回 Update；遇到 Batch 逐个展开。只用于不会阻塞的 Cmd。
func feed(m *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			feed(m, c)
		}
		return
	}
	if msg != nil {
		feed(m, update(m, msg))
	}
}
