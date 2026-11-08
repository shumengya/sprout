package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shumengya/sprout/internal/agent/event"
)

func TestViewBeforeWindowSize(t *testing.T) {
	m := New(Options{Runner: scriptRunner(), Info: testInfo})
	if got := m.View().Content; !strings.Contains(got, "加载中") {
		t.Fatalf("收到窗口尺寸前应显示加载中，得到 %q", got)
	}
}

func TestLayoutFillsTerminalExactly(t *testing.T) {
	for _, sz := range [][2]int{{80, 24}, {40, 12}, {120, 40}, {20, 10}, {200, 50}} {
		m := newSized(t, Options{Runner: scriptRunner()}, sz[0], sz[1])
		// 放进足够多的内容，确认溢出的是 viewport 内部滚动，而不是整个界面。
		for i := 0; i < 60; i++ {
			m.add(&entry{kind: kindAssistant, text: strings.Repeat("长内容", 30)})
		}
		m.refresh()
		lines := strings.Split(m.View().Content, "\n")
		if len(lines) != sz[1] {
			t.Errorf("%dx%d: 画面 %d 行，应恰好 %d 行", sz[0], sz[1], len(lines), sz[1])
		}
		for i, l := range lines {
			if w := ansi.StringWidth(l); w > sz[0] {
				t.Errorf("%dx%d 第 %d 行宽 %d 超出终端宽度: %q", sz[0], sz[1], i, w, ansi.Strip(l))
			}
		}
	}
}

func TestHeaderShowsModelCwdDataDirAndTools(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 24)
	mustContain(t, plain(m), "Sprout", "model: deepseek-chat", `cwd: D:\SmyProjects\sprout`,
		`data: D:\data\.sprout`, "tools: powershell, bash")
}

func TestHeaderWithNoTools(t *testing.T) {
	info := testInfo
	info.Tools = nil
	m := newSized(t, Options{Runner: scriptRunner(), Info: info}, 100, 24)
	mustContain(t, plain(m), "tools: 无")
}

func TestUnavailableToolsAndStartupNoticesShown(t *testing.T) {
	info := testInfo
	info.Unavailable = []string{"bash 不可用：未找到 Git Bash"}
	m := newSized(t, Options{Runner: scriptRunner(), Info: info, Notices: []string{"配置已加载"}}, 100, 24)
	mustContain(t, plain(m), "bash 不可用：未找到 Git Bash", "配置已加载")
}

func TestCancelHintClearsWhenTurnEnds(t *testing.T) {
	// “正在取消…”只针对本轮；本轮结束后不能残留到下次按键才消失。
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 24)
	begin(m, "q")
	m.cancel = func() {}
	m.Update(keyCtrlC)
	mustContain(t, plain(m), "正在取消")
	apply(m, event.TurnFinished{Reason: event.ReasonCancelled})
	mustNotContain(t, plain(m), "正在取消")
	mustContain(t, plain(m), "空闲")
}

func TestOnlyFirstInputLineHasPrompt(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 24)
	m.in.SetValue("第一行\n第二行\n第三行")
	lines := strings.Split(plain(m), "\n")
	input := lines[len(lines)-inputH:]
	prompts := 0
	for _, l := range input {
		if strings.HasPrefix(l, ">") {
			prompts++
		}
	}
	if prompts != 1 || !strings.HasPrefix(input[0], "> 第一行") || !strings.HasPrefix(input[1], "  第二行") {
		t.Fatalf("多行输入应只有首行带提示符：\n%s", strings.Join(input, "\n"))
	}
}

func TestCursorLandsInsideInputBox(t *testing.T) {
	// 光标位置决定输入法候选窗出现在哪，算错了不会报错，只会飘在别处。
	for _, h := range []int{24, 40} {
		m := newSized(t, Options{Runner: scriptRunner()}, 80, h)
		c := m.View().Cursor
		if c == nil {
			t.Fatalf("h=%d: 输入框有焦点时应设置真实光标", h)
		}
		if c.Y != h-inputH || c.X != 2 {
			t.Errorf("h=%d: 空输入时光标 = (%d,%d)，期望 (2,%d)：提示符之后、输入框首行", h, c.X, c.Y, h-inputH)
		}
		m.Update(typed("你"))
		m.Update(typed("a"))
		if c := m.View().Cursor; c.X != 2+2+1 || c.Y != h-inputH { // 汉字占 2 列
			t.Errorf("h=%d: 输入“你a”后光标 = (%d,%d)，期望 (5,%d)", h, c.X, c.Y, h-inputH)
		}
	}
}

func TestStreamingDeltasAppendToOneAssistantEntry(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 24)
	begin(m, "你好")
	apply(m, event.MessageDelta{Text: "你"}, event.MessageDelta{Text: "好呀"})
	if n := countKind(m, kindAssistant); n != 1 {
		t.Fatalf("流式增量应追加到同一个条目，得到 %d 个", n)
	}
	mustContain(t, plain(m), "你> 你好", "你好呀")
	apply(m, event.MessageCompleted{Content: "你好呀"})
	if m.cur != nil || countKind(m, kindAssistant) != 1 {
		t.Fatal("完成后不应再追加，也不应重复创建条目")
	}
}

func TestMessageCompletedWithoutDeltasStillShowsText(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 24)
	begin(m, "q")
	apply(m, event.MessageCompleted{Content: "完整内容"})
	mustContain(t, plain(m), "完整内容")
}

func countKind(m *Model, k kind) int {
	n := 0
	for _, e := range m.entries {
		if e.kind == k {
			n++
		}
	}
	return n
}
