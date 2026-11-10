package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shumengya/sprout/internal/agent/event"
)

// fakeClip 记录写进剪贴板的内容。测试替身，只出现在 _test.go 里。
type fakeClip struct {
	got []string
	err error
}

func (f *fakeClip) write(s string) error { f.got = append(f.got, s); return f.err }

// selModel 返回一个已显示 answer 的 Model，剪贴板换成替身，测试不会碰真实的系统剪贴板。
func selModel(t *testing.T, answer string) (*Model, *fakeClip) {
	t.Helper()
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 24)
	fc := &fakeClip{}
	m.clip = fc.write
	begin(m, "q")
	apply(m, event.MessageDelta{Text: answer})
	return m, fc
}

// selWhere 返回 s 在对话区里的屏幕坐标（x 是显示列）。
func selWhere(t *testing.T, m *Model, s string) (x, y int) {
	t.Helper()
	for i, l := range m.plainLines() {
		if idx := strings.Index(l, s); idx >= 0 {
			return ansi.StringWidth(l[:idx]), i - m.vp.YOffset()
		}
	}
	t.Fatalf("对话里找不到 %q:\n%s", s, strings.Join(m.plainLines(), "\n"))
	return 0, 0
}

// selDrag 模拟按下、拖动、松开，并处理松开后产生的复制命令。
func selDrag(m *Model, x1, y1, x2, y2 int) {
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x1, Y: y1})
	m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: x2, Y: y2})
	_, cmd := m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x2, Y: y2})
	selSettle(m, cmd)
}

// selSettle 执行命令并把复制结果喂回 Update。tea.SetClipboard 的 OSC 52 消息只有运行时会处理，这里忽略。
func selSettle(m *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			selSettle(m, c)
		}
	case copiedMsg:
		m.Update(msg)
	}
}

func TestDragSelectsAndCopies(t *testing.T) {
	m, fc := selModel(t, "hello banana split today")
	x, y := selWhere(t, m, "banana")
	selDrag(m, x, y, x+5, y) // 选区两端各包含光标所在的字符
	if !slices.Equal(fc.got, []string{"banana"}) {
		t.Fatalf("复制的内容 = %q", fc.got)
	}
	mustContain(t, plain(m), "已复制 6 个字符", "hello banana split today")
	if !m.statusOK {
		t.Error("复制成功应使用成功提示色")
	}
}

func TestDragBackwardsSelectsSameText(t *testing.T) {
	m, fc := selModel(t, "hello banana split today")
	x, y := selWhere(t, m, "banana")
	selDrag(m, x+5, y, x, y)
	if !slices.Equal(fc.got, []string{"banana"}) {
		t.Fatalf("反方向拖动复制的内容 = %q", fc.got)
	}
}

func TestDragAcrossLines(t *testing.T) {
	m, fc := selModel(t, "- alpha one\n- beta two\n- gamma three")
	x1, y1 := selWhere(t, m, "beta")
	x2, y2 := selWhere(t, m, "gamma")
	selDrag(m, x1, y1, x2+4, y2)
	if want := "beta two\n• gamma"; !slices.Equal(fc.got, []string{want}) {
		t.Fatalf("复制的内容 = %q，期望 %q", fc.got, want)
	}
}

func TestDragSnapsToWholeWideChars(t *testing.T) {
	m, fc := selModel(t, "前面的中文字符后面")
	x, y := selWhere(t, m, "中文")
	// 起点落在“中”的右半边、终点落在“文”的左半边：都要选中整个字，不能把字劈开。
	selDrag(m, x+1, y, x+2, y)
	if !slices.Equal(fc.got, []string{"中文"}) {
		t.Fatalf("复制的内容 = %q", fc.got)
	}
}

func TestSelectionIsHighlightedWithoutChangingText(t *testing.T) {
	m, fc := selModel(t, "plain **bold** tail")
	x1, y := selWhere(t, m, "plain")
	x2, _ := selWhere(t, m, "tail")
	selDrag(m, x1, y, x2+3, y)

	if !slices.Contains(sgrBefore(t, m.View().Content, "plain"), "7") {
		t.Error("选中的文字应反色显示")
	}
	if ansi.Strip(m.body()) != ansi.Strip(m.vp.View()) {
		t.Error("高亮只改样式，不能改文字")
	}
	if !slices.Equal(fc.got, []string{"plain bold tail"}) || strings.Contains(fc.got[0], "\x1b") {
		t.Fatalf("复制的应是不带样式的纯文本: %q", fc.got)
	}
}

func TestCopyFailureIsReported(t *testing.T) {
	m, fc := selModel(t, "hello banana split today")
	fc.err = errors.New("boom")
	x, y := selWhere(t, m, "banana")
	selDrag(m, x, y, x+5, y)
	if m.statusOK || !strings.Contains(m.status, "boom") {
		t.Fatalf("失败应以警告色提示原因: ok=%v status=%q", m.statusOK, m.status)
	}
	mustContain(t, plain(m), "系统剪贴板写入失败")
}
