package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/shumengya/sprout/internal/agent/event"
)

// infoLine 返回第 i（0-3）行状态栏的纯文本。
func infoText(m *Model, i int) string {
	return ansi.Strip(m.infoLines()[i])
}

func TestFmtElapsed(t *testing.T) {
	for in, want := range map[time.Duration]string{
		-5 * time.Second:                "0s",
		0:                               "0s",
		59 * time.Second:                "59s",
		60 * time.Second:                "1m00s",
		46*time.Minute + 43*time.Second: "46m43s",
		time.Hour:                       "1h00m00s",
		time.Hour + 2*time.Minute + 3*time.Second: "1h02m03s",
		26*time.Hour + 5*time.Second:              "26h00m05s",
	} {
		if got := fmtElapsed(in); got != want {
			t.Errorf("fmtElapsed(%v) = %q，期望 %q", in, got, want)
		}
	}
}

func TestInputExcludesCacheSoThatInputPlusCacheIsTheWholeInput(t *testing.T) {
	// 服务端报告的输入含缓存。“输入”只算没命中缓存的部分，和“缓存”加起来才是全部输入，
	// 不然同一批 token 在状态栏上被算了两遍。
	m := newSized(t, Options{Runner: scriptRunner()}, 200, 24)
	apply(m, event.Usage{InputTokens: 1000, OutputTokens: 50, CacheReadTokens: 800, CacheWriteTokens: 100})
	line := infoText(m, 1)
	for _, want := range []string{"输入 100", "输出 50", "缓存 800/100"} {
		if !strings.Contains(line, want) {
			t.Errorf("缺少 %q: %q", want, line)
		}
	}
}

func TestInputNeverGoesNegativeWhenTheServerReportsMoreCacheThanInput(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 200, 24)
	apply(m, event.Usage{InputTokens: 100, CacheReadTokens: 300})
	if line := infoText(m, 1); !strings.Contains(line, "输入 0") || strings.Contains(line, "输入 -") {
		t.Errorf("口径对不上的数据不能让输入变成负数: %q", line)
	}
}

func TestUsageTotalsAccumulateAcrossCalls(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 200, 24)
	apply(m,
		event.Usage{InputTokens: 100, OutputTokens: 10, CacheReadTokens: 40},
		event.Usage{InputTokens: 200, OutputTokens: 20, CacheReadTokens: 150, CacheWriteTokens: 5},
	)
	if m.usage != (usageTotals{Input: 105, Output: 30, CacheRead: 190, CacheWrite: 5}) {
		t.Errorf("usage = %+v", m.usage)
	}
}

func TestContextUsesTheLatestRequestNotTheSum(t *testing.T) {
	// 上下文占用是下一次请求大致要带的历史长度，取最近一次请求的输入，而不是累加。
	info := testInfo
	info.ContextWindow = 1000
	m := newSized(t, Options{Runner: scriptRunner(), Info: info}, 200, 24)
	apply(m, event.Usage{InputTokens: 300}, event.Usage{InputTokens: 500})
	if line := infoText(m, 1); !strings.Contains(line, "上下文 50.0%/1k") {
		t.Errorf("上下文应是最近一次的 500/1000: %q", line)
	}
	// 服务端没报告用量（0）时保持原值，不能把占用清零。
	apply(m, event.Usage{OutputTokens: 5})
	if line := infoText(m, 1); !strings.Contains(line, "50.0%") {
		t.Errorf("没报告输入时应保持原来的占用: %q", line)
	}
}

func TestContextColorsWarnNearTheLimit(t *testing.T) {
	info := testInfo
	info.ContextWindow = 1000
	own := "\x1b[38;5;" + labelColors["上下文"] + "m"
	cases := []struct {
		input int
		sgr   string // 空串表示值和标签同色，不带警告色
	}{
		{500, ""},
		{700, "\x1b[33m"}, // 黄
		{950, "\x1b[31m"}, // 红
	}
	for _, c := range cases {
		m := newSized(t, Options{Runner: scriptRunner(), Info: info}, 200, 24)
		apply(m, event.Usage{InputTokens: c.input})
		ctx := m.contextSeg().render()
		if !strings.Contains(ctx, own+"上下文") {
			t.Errorf("%d/1000 标签应保持自己的颜色: %q", c.input, ctx)
		}
		if c.sgr == "" && (strings.Contains(ctx, "\x1b[33m") || strings.Contains(ctx, "\x1b[31m") ||
			!strings.Contains(ctx, own+"上下文 50.0%")) {
			t.Errorf("%d/1000 值应与标签同色，不带警告色: %q", c.input, ctx)
		}
		if c.sgr != "" && !strings.Contains(ctx, c.sgr) {
			t.Errorf("%d/1000 应着色 %q: %q", c.input, c.sgr, ctx)
		}
	}
}

func TestContextWithoutAKnownWindowShowsTheTokenCount(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 200, 24) // testInfo 没有窗口
	if v := m.contextSeg().value; v != placeholder {
		t.Errorf("没有数据也没有窗口时应是占位符，得到 %q", v)
	}
	apply(m, event.Usage{InputTokens: 2900})
	if v := m.contextSeg().value; v != "2.9k" || strings.Contains(v, "%") {
		t.Errorf("不知道窗口时只显示 token 数，不能编一个百分比: %q", v)
	}
}

func TestCompactedUpdatesTheContextAndAFailureDoesNot(t *testing.T) {
	info := testInfo
	info.ContextWindow = 10000
	m := newSized(t, Options{Runner: scriptRunner(), Info: info}, 200, 24)
	apply(m, event.Usage{InputTokens: 8000})
	apply(m, event.Compacted{Mode: event.CompactSummary, Dropped: 12, TokensBefore: 8000, TokensAfter: 1500})
	if m.ctxTokens != 1500 {
		t.Errorf("压缩之后占用应是压缩后的估算: %d", m.ctxTokens)
	}
	// 摘要失败时历史没动，占用还是原来的；事件里的 TokensAfter 只是 TokensBefore 的原样复制。
	apply(m, event.Usage{InputTokens: 6000})
	apply(m, event.Compacted{Mode: event.CompactSummary, TokensBefore: 6000, TokensAfter: 6000, Err: "失败"})
	if m.ctxTokens != 6000 {
		t.Errorf("压缩失败不该改占用: %d", m.ctxTokens)
	}
	// 手动压缩同样要更新。
	apply(m, event.Compacted{Mode: event.CompactSummary, Manual: true, Dropped: 4, TokensBefore: 6000, TokensAfter: 700})
	if m.ctxTokens != 700 {
		t.Errorf("手动压缩之后占用 = %d", m.ctxTokens)
	}
}

func TestUnimplementedFeaturesShowAPlaceholderNotZero(t *testing.T) {
	// MCP、插件、技能、花费还没有实现。显示 0 会让人以为它们已经接上、只是没有内容。
	m := newSized(t, Options{Runner: scriptRunner()}, 200, 24)
	apply(m, event.Usage{InputTokens: 100, OutputTokens: 100})
	if line := infoText(m, 2); !strings.Contains(line, "MCP —") || !strings.Contains(line, "插件 —") || !strings.Contains(line, "技能 —") {
		t.Errorf("MCP / 插件 / 技能 应是占位符: %q", line)
	}
	if line := infoText(m, 1); !strings.Contains(line, "花费 —") {
		t.Errorf("花费应是占位符，不能算一个数出来: %q", line)
	}
}

func TestToolCountMatchesTheRegisteredTools(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 200, 24)
	if line := infoText(m, 2); !strings.Contains(line, "工具 2") {
		t.Errorf("testInfo 有两个工具: %q", line)
	}
	m.opts.Info.Tools = append(m.opts.Info.Tools, ToolInfo{Name: "read"}, ToolInfo{Name: "write"})
	if line := infoText(m, 2); !strings.Contains(line, "工具 4") {
		t.Errorf("切换模型后工具数应跟着变: %q", line)
	}
}

func TestTurnsCountUserMessagesOnly(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 200, 24)
	begin(m, "第一问")
	apply(m, event.MessageCompleted{Content: "答"}, event.TurnFinished{Reason: event.ReasonStop})
	submit(m, "/help") // 本地命令不是一轮对话
	begin(m, "第二问")
	if line := infoText(m, 0); !strings.Contains(line, "对话轮次 2") {
		t.Errorf("只有用户提问算一轮: %q", line)
	}
}

func TestElapsedCountsOnlyBusyTime(t *testing.T) {
	f := &fakeSessions{}
	m := sessionsModel(t, f)
	start := m.now()
	clock := start
	m.now = func() time.Time { return clock }
	// 空闲时无论过多久都不涨。
	clock = start.Add(10 * time.Minute)
	if line := infoText(m, 0); !strings.Contains(line, "耗时 0s") {
		t.Fatalf("没在干活时耗时不应增长: %q", line)
	}
	// 一轮回答期间按时钟累计，结束后定格。
	begin(m, "第一问")
	m.busyStart()
	clock = start.Add(10*time.Minute + 51*time.Second)
	if line := infoText(m, 0); !strings.Contains(line, "耗时 51s") {
		t.Fatalf("回答期间耗时应随时钟走: %q", line)
	}
	apply(m, event.TurnFinished{Reason: event.ReasonStop})
	clock = start.Add(20 * time.Minute)
	if line := infoText(m, 0); !strings.Contains(line, "耗时 51s") {
		t.Errorf("回答结束后耗时应停住: %q", line)
	}
	// 第二轮接在第一轮后面累加。
	begin(m, "第二问")
	m.busyStart()
	clock = start.Add(20*time.Minute + 12*time.Second)
	if line := infoText(m, 0); !strings.Contains(line, "耗时 1m03s") {
		t.Errorf("多轮耗时应累加: %q", line)
	}
	// 生成中拒绝 /new，先结束本轮再新建。
	apply(m, event.TurnFinished{Reason: event.ReasonStop})
	submit(m, "/new")
	if line := infoText(m, 0); !strings.Contains(line, "耗时 0s") {
		t.Errorf("/new 之后应重新计时: %q", line)
	}
}

func TestElapsedIsNeverNegative(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 200, 24)
	m.busySince = m.now().Add(time.Hour) // 时钟回拨
	if line := infoText(m, 0); !strings.Contains(line, "耗时 0s") {
		t.Errorf("耗时不能是负数: %q", line)
	}
	m.busySince = time.Time{}
	if line := infoText(m, 0); !strings.Contains(line, "耗时 0s") {
		t.Errorf("空闲时是 0s，而不是从公元 1 年算起: %q", line)
	}
}

func TestResourcesAreShownOnlyWhenKnown(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 200, 24)
	line := infoText(m, 0)
	if strings.Contains(line, "内存") || strings.Contains(line, "CPU") {
		t.Errorf("还没采样到时不显示这两项，不能显示 0%%: %q", line)
	}
	m.res = Resources{Mem: 43.4, CPU: 1.2}
	if line := infoText(m, 0); !strings.Contains(line, "内存 43%") || !strings.Contains(line, "CPU 1%") {
		t.Errorf("采样到之后显示取整的百分比: %q", line)
	}
	// 只读到内存（比如 Termux 读不了 /proc/stat）：CPU 不显示，内存照常。
	m.res = Resources{Mem: 60, CPU: -1}
	if line := infoText(m, 0); !strings.Contains(line, "内存 60%") || strings.Contains(line, "CPU") {
		t.Errorf("只读到内存时不显示 CPU: %q", line)
	}
}

func TestResourceTickStoresTheSampleAndSchedulesTheNext(t *testing.T) {
	calls := 0
	o := Options{Runner: scriptRunner(), Resources: func() Resources { calls++; return Resources{Mem: 50, CPU: 10} }}
	m := newSized(t, o, 200, 24)

	msg := m.resourceCmd(0)() // 立刻采一次
	if calls != 1 {
		t.Fatalf("应调用采样函数一次: %d", calls)
	}
	_, next := m.Update(msg)
	if m.res != (Resources{Mem: 50, CPU: 10}) {
		t.Errorf("采样结果应存下来: %+v", m.res)
	}
	if next == nil {
		t.Fatal("收到采样后应约定下一次采样，否则状态栏只刷新一次")
	}
}

func TestResourceCmdWithoutASamplerReportsUnknown(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 200, 24)
	rm, ok := m.resourceCmd(0)().(resourceMsg)
	if !ok || rm.res.Mem >= 0 || rm.res.CPU >= 0 {
		t.Errorf("没有采样函数时应报告读不到（负数），不能是 0: %+v ok=%v", rm.res, ok)
	}
}

func TestStatusNameFollowsWhatTheProgramIsDoing(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner(), Compactor: &fakeCompactor{}}, 200, 24)
	check := func(want string) {
		t.Helper()
		if line := infoText(m, 2); !strings.Contains(line, "状态 "+want) {
			t.Errorf("状态应是 %q: %q", want, line)
		}
	}
	check("空闲")

	begin(m, "q")
	check("生成中")
	apply(m, event.ToolRequested{Call: event.ToolCall{ID: "1", Name: "bash", Arguments: `{"command":"ls"}`}})
	check("执行 bash")
	apply(m, event.ToolRequested{Call: event.ToolCall{ID: "2", Name: "read", Arguments: `{}`}})
	check("并行执行 2 个工具")
	apply(m, event.TurnFinished{Reason: event.ReasonStop})
	check("空闲")

	m.resuming = true
	check("恢复会话")
	m.resuming, m.switching = false, true
	check("切换模型")
	m.switching, m.compacting = false, true
	check("压缩上下文")
}

func TestNewSessionAndResumeRebuildTheStatusBar(t *testing.T) {
	// 恢复会话时用量由重放的 usage 事件重新累计，不是沿用恢复前的数字。
	f := &fakeSessions{res: &Restored{Events: []event.Event{
		event.TurnStarted{Prompt: "旧问题"},
		event.Usage{InputTokens: 1000, OutputTokens: 40, CacheReadTokens: 900},
		event.TurnFinished{Reason: event.ReasonStop},
	}, Notice: "已恢复"}}
	m := sessionsModel(t, f)
	m.usage = usageTotals{Input: 99999, Output: 99999}
	m.ctxTokens = 99999
	m.in.SetValue("/resume 1")
	_, cmd := m.Update(keyEnter)
	runCmd(m, cmd)
	if m.usage != (usageTotals{Input: 100, Output: 40, CacheRead: 900}) || m.ctxTokens != 1000 {
		t.Errorf("恢复后应只含重放出来的用量: %+v ctx=%d", m.usage, m.ctxTokens)
	}
	if line := infoText(m, 0); !strings.Contains(line, "对话轮次 1") {
		t.Errorf("恢复后轮次应由重放出的提问算出: %q", line)
	}
}

func TestSwitchingModelUpdatesTheContextWindow(t *testing.T) {
	models := testModels()
	models.res = &SwitchResult{Info: Info{Model: "two", Provider: "b", ContextWindow: 200000}, Notice: "已切换"}
	info := testInfo
	info.ContextWindow = 1000
	m := newSized(t, Options{Runner: scriptRunner(), Models: models, Info: info}, 200, 30)
	apply(m, event.Usage{InputTokens: 500})
	if line := infoText(m, 1); !strings.Contains(line, "50.0%/1k") {
		t.Fatalf("切换前: %q", line)
	}
	m.in.SetValue("/model b/two")
	_, cmd := m.Update(keyEnter)
	runCmd(m, cmd)
	if line := infoText(m, 1); !strings.Contains(line, "0.2%/200k") || !strings.Contains(line, "模型ID two") {
		t.Errorf("切换后百分比应按新窗口重算: %q", line)
	}
}

func TestRealProgramTicksEveryNowAndThenWithoutBlockingInput(t *testing.T) {
	// 采样函数很慢也不能卡住界面：它在 tea.Cmd 里执行，不在 Update 里。
	slow := make(chan struct{})
	o := Options{Runner: scriptRunner(), Resources: func() Resources { <-slow; return Resources{Mem: 1, CPU: 1} }}
	m := newSized(t, o, 100, 24)
	init := m.Init()
	if init == nil {
		t.Fatal("Init 应返回命令")
	}
	// Init 返回的是 Batch：聚焦输入框和首次采样并发执行。采样阻塞着，输入框仍然可以正常工作。
	typeText(m, "你好")
	if got := m.in.Value(); got != "你好" {
		t.Errorf("采样阻塞时输入框也该能用: %q", got)
	}
	close(slow)
}
