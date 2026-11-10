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

func TestInfoLineIsAtTheBottomInChinese(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 24)
	lines := strings.Split(plain(m), "\n")
	last := strings.TrimRight(lines[len(lines)-1], " ")
	for _, want := range []string{"模型 deepseek-chat", `目录 D:\SmyProjects\sprout`, "工具 powershell、bash"} {
		if !strings.Contains(last, want) {
			t.Errorf("最后一行应是中文信息栏，缺少 %q: %q", want, last)
		}
	}
	// 英文标签和数据目录都不再占信息栏；数据目录在 /tools 里看。
	mustNotContain(t, last, "model:", "cwd:", "tools:", "data:", `D:\data`)
}

func TestTopOfScreenIsNowTheTranscript(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 24)
	first := strings.Split(plain(m), "\n")[0]
	if !strings.Contains(first, "输入问题后按 Enter 发送") {
		t.Fatalf("顶部第一行应直接是对话区内容，不再有信息栏: %q", first)
	}
	// 去掉顶部 4 行后，对话区更高。
	if want := 24 - footerH; m.vp.Height() != want {
		t.Errorf("对话区高度 = %d，期望 %d", m.vp.Height(), want)
	}
}

func TestInfoLineWithNoTools(t *testing.T) {
	info := testInfo
	info.Tools = nil
	m := newSized(t, Options{Runner: scriptRunner(), Info: info}, 100, 24)
	lines := strings.Split(plain(m), "\n")
	if last := lines[len(lines)-1]; !strings.Contains(last, "工具 无") {
		t.Errorf("没有可用工具时显示“无”: %q", last)
	}
}

func TestInfoLineFieldsHaveDistinctColors(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 24)
	line := m.infoLine()
	// 三个值各用一种前景色，标签是淡色：模型绿、目录亮蓝、工具洋红。
	for name, sgr := range map[string]string{"模型": "\x1b[32m", "目录": "\x1b[94m", "工具": "\x1b[35m"} {
		if !strings.Contains(line, sgr) {
			t.Errorf("%s 的值缺少颜色 %q: %q", name, sgr, line)
		}
	}
	if !strings.Contains(line, "\x1b[2m") {
		t.Errorf("标签应是淡色: %q", line)
	}
}

func TestInfoLineTruncatesLongDirectoryFromTheLeft(t *testing.T) {
	long := `D:\Projects\very\deep\nested\folder\structure\that\goes\on\and\on\myproject`
	info := testInfo
	info.Cwd = long
	m := newSized(t, Options{Runner: scriptRunner(), Info: info}, 70, 24)
	lines := strings.Split(plain(m), "\n")
	last := strings.TrimRight(lines[len(lines)-1], " ")
	if !strings.Contains(last, "…") || !strings.HasSuffix(strings.Split(last, "  工具")[0], `\myproject`) {
		t.Errorf("过长的目录应从左边截断，保留末尾: %q", last)
	}
	if strings.Contains(last, `D:\Projects`) {
		t.Errorf("开头部分应被截掉: %q", last)
	}
	// 模型和工具不能为了让路给目录而被截掉。
	if !strings.Contains(last, "模型 deepseek-chat") || !strings.Contains(last, "工具 powershell、bash") {
		t.Errorf("模型和工具应完整保留: %q", last)
	}
}

func TestInfoLineNeverExceedsWidth(t *testing.T) {
	info := testInfo
	info.Cwd = strings.Repeat(`很长的目录\`, 20)
	for _, w := range []int{10, 20, 30, 40, 60, 80, 120} {
		m := newSized(t, Options{Runner: scriptRunner(), Info: info}, w, 24)
		if got := ansi.StringWidth(m.infoLine()); got > w {
			t.Errorf("宽度 %d 下信息栏宽 %d 超出终端宽度", w, got)
		}
	}
}

func TestTruncateLeft(t *testing.T) {
	cases := []struct {
		in   string
		w    int
		want string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{`D:\a\b\c\d`, 6, `…\c\d`},
		{"目录很长很长很长", 7, "…长很长"},
	}
	for _, c := range cases {
		got := truncateLeft(c.in, c.w)
		if ansi.StringWidth(got) > c.w || !(got == c.in || strings.HasPrefix(got, "…")) {
			t.Errorf("truncateLeft(%q, %d) = %q，宽度或形式不对", c.in, c.w, got)
		}
	}
	if got := truncateLeft(`D:\a\b\c\d`, 6); !strings.HasSuffix(got, `\c\d`) {
		t.Errorf("应保留末尾: %q", got)
	}
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
	input := lines[len(lines)-1-inputH : len(lines)-1] // 最后一行是信息栏
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
		// 输入框在信息栏上面：光标 Y = 总高 - 信息栏 1 行 - 输入框 inputH 行。
		wantY := h - 1 - inputH
		if c.Y != wantY || c.X != 2 {
			t.Errorf("h=%d: 空输入时光标 = (%d,%d)，期望 (2,%d)：提示符之后、输入框首行", h, c.X, c.Y, wantY)
		}
		m.Update(typed("你"))
		m.Update(typed("a"))
		if c := m.View().Cursor; c.X != 2+2+1 || c.Y != wantY { // 汉字占 2 列
			t.Errorf("h=%d: 输入“你a”后光标 = (%d,%d)，期望 (5,%d)", h, c.X, c.Y, wantY)
		}
	}
}

func TestUserAndAssistantDifferByColorNotPrefix(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 30)
	begin(m, "用户的话")
	apply(m, event.MessageDelta{Text: "助手的话"})

	var user, assistant string
	for _, e := range m.entries {
		switch e.kind {
		case kindUser:
			user = e.render(80)
		case kindAssistant:
			assistant = e.render(80)
		}
	}
	// 两者都不带前缀，纯文本就是原话。
	if ansi.Strip(user) != "用户的话" || ansi.Strip(assistant) != "助手的话" {
		t.Fatalf("不应有前缀: %q / %q", ansi.Strip(user), ansi.Strip(assistant))
	}
	// 区分只靠颜色：用户消息带颜色转义，助手回答不带前景色转义。
	if !strings.Contains(user, "\x1b[") {
		t.Errorf("用户消息应带颜色: %q", user)
	}
	if strings.Contains(assistant, "\x1b[3") {
		t.Errorf("助手回答应使用终端默认前景色，不应带前景色转义: %q", assistant)
	}
	if user == assistant {
		t.Error("两种消息的渲染不应相同")
	}
}

func TestNoEmojiOrChatPrefixesInInterface(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner(), Info: Info{
		Model: "m", Tools: []ToolInfo{{"bash", "x"}}, Unavailable: []string{"powershell 不可用：原因"},
	}}, 100, 40)
	begin(m, "问题")
	apply(m, requested("c", "bash", `{"command":"ls"}`),
		event.ToolFinished{ID: "c", Name: "bash", Stdout: "a\n"}, event.MessageDelta{Text: "答"})
	submit(m, "/tools")
	s := plain(m)
	for _, bad := range []string{"⚙", "✗", "你> ", "🔧", "✅", "❌"} {
		if strings.Contains(s, bad) {
			t.Errorf("界面里不应出现 %q:\n%s", bad, s)
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
	mustContain(t, plain(m), "你好", "你好呀")
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
