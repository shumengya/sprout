package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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

func TestInfoLinesAreAtTheBottomInChinese(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 120, 24)
	lines := strings.Split(plain(m), "\n")
	info := lines[len(lines)-infoRows:]
	for i, wants := range [][]string{
		{"路径 " + `D:\SmyProjects\sprout`, "对话轮次 0", "耗时 0s"},
		{"输入 0", "输出 0", "缓存 0/0", "花费 —", "上下文 —", "模型 deepseek-flash"},
		{"状态 空闲", "MCP —", "插件 —", "技能 —", "工具 2"},
	} {
		for _, want := range wants {
			if !strings.Contains(info[i], want) {
				t.Errorf("第 %d 行状态栏缺少 %q: %q", i+1, want, info[i])
			}
		}
	}
	// 英文标签和数据目录都不占状态栏；数据目录在 /tools 里看。
	mustNotContain(t, strings.Join(info, "\n"), "model:", "cwd:", "tools:", "data:", `D:\data`)
}

func TestTopOfScreenIsNowTheTranscript(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 24)
	first := strings.Split(plain(m), "\n")[0]
	if !strings.Contains(first, "输入问题后按 Enter 发送") {
		t.Fatalf("顶部第一行应直接是对话区内容，不再有信息栏: %q", first)
	}
	// 空闲时底部有 6 行：分隔线、一行输入框、分隔线、三行状态栏。
	if want := 24 - (3 + infoRows); m.vp.Height() != want {
		t.Errorf("对话区高度 = %d，期望 %d", m.vp.Height(), want)
	}
}

func TestFooterIsCompactWhenIdle(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 24)
	lines := strings.Split(plain(m), "\n")
	foot := lines[len(lines)-(3+infoRows):]
	if !strings.HasPrefix(foot[0], "────") || !strings.HasPrefix(foot[1], "❯ 输入消息") ||
		!strings.HasPrefix(foot[2], "────") || !strings.Contains(foot[3], "路径") ||
		!strings.Contains(foot[4], "模型 deepseek-flash") || !strings.Contains(foot[5], "状态 空闲") {
		t.Fatalf("空闲时底部应是 分隔线 / 输入框 / 分隔线 / 三行状态栏:\n%s", strings.Join(foot, "\n"))
	}
	mustNotContain(t, plain(m), "Ctrl+C 退出")
}

func TestActivityLineAppearsOnlyWhileRunning(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 24)
	idle := m.vp.Height()
	begin(m, "q")
	if m.vp.Height() != idle-1 {
		t.Fatalf("运行时多一行活动行，对话区应矮一行: %d → %d", idle, m.vp.Height())
	}
	lines := strings.Split(plain(m), "\n")
	// 活动行在 分隔线 / 输入框 / 分隔线 / 三行状态栏 的上面。
	if act := lines[len(lines)-(4+infoRows)]; !strings.Contains(act, "生成中") || !strings.Contains(act, "Esc 取消") {
		t.Fatalf("输入框上方应是活动行: %q", act)
	}
	apply(m, event.TurnFinished{Reason: event.ReasonStop})
	if m.vp.Height() != idle {
		t.Fatalf("结束后活动行消失，对话区恢复高度: %d", m.vp.Height())
	}
}

func TestInputGrowsWithContentAndShrinksAfterSend(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 24)
	idle := m.vp.Height()
	m.Update(typed("一"))
	for i := 0; i < 2; i++ {
		m.Update(press('j', tea.ModCtrl)) // Ctrl+J 换行
		m.Update(typed("行"))
	}
	if m.in.Height() != 3 || m.vp.Height() != idle-2 {
		t.Fatalf("三行输入应占三行: 输入框 %d 行，对话区 %d（空闲 %d）", m.in.Height(), m.vp.Height(), idle)
	}
	for i := 0; i < 20; i++ {
		m.Update(press('j', tea.ModCtrl))
		m.Update(typed("行"))
	}
	if m.in.Height() != inputMaxRows || len(strings.Split(m.in.Value(), "\n")) != 23 {
		t.Fatalf("超过上限后只显示 %d 行，但内容不能被卡住: 显示 %d 行，内容 %d 行",
			inputMaxRows, m.in.Height(), len(strings.Split(m.in.Value(), "\n")))
	}
	if len(strings.Split(m.View().Content, "\n")) != 24 {
		t.Fatal("输入框增高时整个画面仍应恰好填满终端")
	}
	submit(m, "/help")
	if m.in.Height() != 1 || m.vp.Height() != idle {
		t.Fatalf("发送后输入框回到一行: 输入框 %d 行，对话区 %d", m.in.Height(), m.vp.Height())
	}
}

func TestUsageShownOnTheSecondInfoLine(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 120, 24)
	apply(m, event.Usage{InputTokens: 2000, OutputTokens: 863, TotalTokens: 2863})
	lines := strings.Split(plain(m), "\n")
	line := lines[len(lines)-infoRows+1]
	if !strings.Contains(line, "输入 2k") || !strings.Contains(line, "输出 863") {
		t.Fatalf("累计用量应在第二行状态栏: %q", line)
	}
	// 太窄时整行从右边截断，不能超宽，左边的先保留。
	narrow := newSized(t, Options{Runner: scriptRunner()}, 30, 24)
	apply(narrow, event.Usage{InputTokens: 2000, OutputTokens: 863})
	got := narrow.infoLines()[1]
	if ansi.StringWidth(got) > 30 || !strings.HasPrefix(ansi.Strip(got), "输入 2k") {
		t.Fatalf("窄宽度下第二行: %q", ansi.Strip(got))
	}
}

func TestInfoLineWithNoToolsShowsZero(t *testing.T) {
	info := testInfo
	info.Tools = nil
	m := newSized(t, Options{Runner: scriptRunner(), Info: info}, 100, 24)
	lines := strings.Split(plain(m), "\n")
	if last := lines[len(lines)-1]; !strings.Contains(last, "工具 0") {
		t.Errorf("没有可用工具时显示 0: %q", last)
	}
}

func TestInfoLabelsHaveDistinctColors(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 200, 24)
	m.res = Resources{Mem: 43, CPU: 1}
	all := strings.Join(m.infoLines(), "\n")
	seen := map[string]string{}
	for label, c := range labelColors {
		sgr := "\x1b[38;5;" + c + "m"
		if _, dup := seen[c]; dup {
			t.Errorf("颜色 %s 被 %q 和 %q 重复使用", c, seen[c], label)
		}
		seen[c] = label
		if !strings.Contains(all, label) {
			continue // 思考强度只在声明了 effort 时显示
		}
		// 标签和值必须用同一种颜色：标签名出现在它自己的 SGR 作用域内。
		if !strings.Contains(all, sgr+label) {
			t.Errorf("%q 没有用它登记的颜色 %s 渲染: %q", label, c, all)
		}
	}
	// 项之间用淡色竖线分隔。
	if !strings.Contains(all, " "+styFaint.Render(segSep)+" ") {
		t.Errorf("项之间应有淡色竖线: %q", all)
	}
}

func TestInfoPathTruncatesLongDirectoryFromTheLeft(t *testing.T) {
	long := `D:\Projects\very\deep\nested\folder\structure\that\goes\on\and\on\myproject`
	info := testInfo
	info.Cwd = long
	m := newSized(t, Options{Runner: scriptRunner(), Info: info}, 70, 24)
	line := ansi.Strip(m.infoLines()[0])
	if !strings.Contains(line, "…") || !strings.Contains(strings.SplitN(line, "  ", 2)[0], `\myproject`) {
		t.Errorf("过长的目录应从左边截断，保留末尾: %q", line)
	}
	if strings.Contains(line, `D:\Projects`) {
		t.Errorf("开头部分应被截掉: %q", line)
	}
	// 轮次和耗时不能为了给路径让位而被截掉。
	if !strings.Contains(line, "对话轮次 0") || !strings.Contains(line, "耗时 0s") {
		t.Errorf("轮次和耗时应完整保留: %q", line)
	}
}

func TestInfoLinesNeverExceedWidth(t *testing.T) {
	info := testInfo
	info.Cwd = strings.Repeat(`很长的目录\`, 20)
	info.Model = strings.Repeat("长模型名", 10)
	for _, w := range []int{10, 20, 30, 40, 60, 80, 120} {
		m := newSized(t, Options{Runner: scriptRunner(), Info: info, Resources: func() Resources { return Resources{Mem: 43, CPU: 1} }}, w, 24)
		m.res = Resources{Mem: 43, CPU: 1}
		for i, l := range m.infoLines() {
			if got := ansi.StringWidth(l); got > w {
				t.Errorf("宽度 %d 下第 %d 行状态栏宽 %d 超出终端宽度", w, i+1, got)
			}
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
	mustNotContain(t, plain(m), "正在取消", "Esc 取消")
	if m.running {
		t.Fatal("本轮结束后应不在运行")
	}
}

func TestOnlyFirstInputLineHasPrompt(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 24)
	m.in.SetValue("第一行\n第二行\n第三行")
	m.fit()
	lines := strings.Split(plain(m), "\n")
	input := lines[len(lines)-(1+infoRows)-3 : len(lines)-(1+infoRows)] // 下面是分隔线和三行状态栏
	prompts := 0
	for _, l := range input {
		if strings.HasPrefix(l, "❯") {
			prompts++
		}
	}
	if prompts != 1 || !strings.HasPrefix(input[0], "❯ 第一行") || !strings.HasPrefix(input[1], "  第二行") {
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
		// 输入框下面是分隔线和三行状态栏：光标 Y = 总高 - (1 + infoRows) - 输入框 1 行。
		wantY := h - (1 + infoRows) - 1
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

func TestCursorFollowsActivityLineAndInputGrowth(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 24)
	begin(m, "q") // 活动行出现：它占的是对话区的一行，输入框位置不变
	if c := m.View().Cursor; c.Y != 24-(1+infoRows)-1 {
		t.Errorf("运行时光标 Y = %d，期望 %d", c.Y, 24-(1+infoRows)-1)
	}
	m.Update(typed("a"))
	m.Update(press('j', tea.ModCtrl))
	m.Update(typed("b"))
	// 两行输入：输入框往上长一行，光标在它的第二行，也就是紧贴下面分隔线的那一行。
	if c := m.View().Cursor; c.Y != 24-(1+infoRows)-1 {
		t.Errorf("第二行输入时光标 Y = %d，期望 %d", c.Y, 24-(1+infoRows)-1)
	}
	lines := strings.Split(m.View().Content, "\n")
	if len(lines) != 24 {
		t.Errorf("画面 %d 行，应恰好 24 行", len(lines))
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
