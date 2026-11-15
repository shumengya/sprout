package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/shumengya/sprout/internal/agent/event"
)

// 用指定布局构造一个 Model；不传布局时用内置默认。
func layoutModel(t *testing.T, items []StatusItem) *Model {
	t.Helper()
	return newSized(t, Options{Runner: scriptRunner(), Statusline: items}, 200, 24)
}

func TestDefaultLayoutIsTheThreeRowLayout(t *testing.T) {
	m := layoutModel(t, nil)
	lines := m.infoLines()
	if len(lines) != infoRows {
		t.Fatalf("默认布局应占 %d 行，得到 %d", infoRows, len(lines))
	}
	wants := [][]string{
		{"路径", "对话轮次", "耗时", "内存", "CPU"},
		{"输入", "输出", "缓存", "花费", "上下文", "模型", "思考强度"},
		{"状态", "速度", "压缩", "MCP", "插件", "技能", "工具", "Git"},
	}
	for i, w := range wants {
		got := ansi.Strip(lines[i])
		for _, label := range w {
			// 能读到数据的项必须出现；没有数据的（内存、CPU、速度、Git）本来就整项不显示。
			if label == "内存" || label == "CPU" || label == "速度" || label == "Git" || label == "思考强度" {
				continue
			}
			if !strings.Contains(got, label) {
				t.Errorf("第 %d 行缺少 %q: %q", i+1, label, got)
			}
		}
	}
	// 默认布局里没有单独一行的“进程内存”，它已经并进“内存”。
	if strings.Contains(strings.Join(lines, "\n"), "进程内存") {
		t.Error("进程内存应并进内存一项")
	}
}

func TestLayoutMovesAndOrdersItems(t *testing.T) {
	m := layoutModel(t, []StatusItem{
		{Name: "输出", Line: 1, Enabled: true},
		{Name: "输入", Line: 1, Enabled: true},
	})
	lines := m.infoLines()
	if len(lines) != 1 {
		t.Fatalf("只剩一行的布局应只占一行，得到 %d: %q", len(lines), lines)
	}
	got := ansi.Strip(lines[0])
	if !strings.HasPrefix(got, "输出 0  输入 0") {
		t.Fatalf("同一行的先后应按配置顺序: %q", got)
	}
	if strings.Contains(got, "路径") || strings.Contains(got, "状态") {
		t.Errorf("没写的项不该显示: %q", got)
	}
}

func TestLayoutCanHideAnItem(t *testing.T) {
	enabled := true
	disabled := false
	items := []StatusItem{
		{Name: "模型", Line: 1, Enabled: enabled},
		{Name: "输入", Line: 1, Enabled: disabled},
	}
	if got := ResolveStatusline(items); len(got) != 1 || got[0].Name != "模型" {
		t.Fatalf("enable 为 false 的项应被丢掉: %+v", got)
	}
	m := layoutModel(t, items)
	if line := ansi.Strip(strings.Join(m.infoLines(), "\n")); strings.Contains(line, "输入") {
		t.Errorf("被禁用的项不该显示: %q", line)
	}
}

func TestLayoutUsesCustomColorAndFallsBackToBuiltin(t *testing.T) {
	m := layoutModel(t, []StatusItem{
		{Name: "状态", Line: 1, Color: "200", Enabled: true},
		{Name: "工具", Line: 1, Enabled: true}, // 没写颜色 → 内置 215
	})
	all := strings.Join(m.infoLines(), "\n")
	if !strings.Contains(all, "\x1b[38;5;200m状态") {
		t.Errorf("配置的颜色应生效: %q", all)
	}
	if !strings.Contains(all, "\x1b[38;5;"+labelColors["工具"]+"m工具") {
		t.Errorf("没写颜色时应用内置颜色: %q", all)
	}
}

func TestLayoutLineIsClampedToTheBuiltinOne(t *testing.T) {
	// line 越界时 ValidateStatusline 会报出来；这里只确认渲染不会因此 panic 或丢项。
	m := layoutModel(t, []StatusItem{{Name: "状态", Line: 9, Enabled: true}})
	if got := ansi.Strip(strings.Join(m.infoLines(), "\n")); !strings.Contains(got, "状态") {
		t.Errorf("行号越界时退回内置行号，不能把项弄丢: %q", got)
	}
}

func TestRowsWithoutVisibleItemsAreDropped(t *testing.T) {
	// 第一行只放了内存，而内存还没采样到：这一行整行省略，不占一行空白。
	m := layoutModel(t, []StatusItem{
		{Name: "内存", Line: 2, Enabled: true},
		{Name: "状态", Line: 3, Enabled: true},
	})
	lines := m.infoLines()
	if len(lines) != 1 || !strings.Contains(ansi.Strip(lines[0]), "状态") {
		t.Fatalf("没有内容的行应省略: %q", lines)
	}
	// 行数变了，对话区高度要跟着变。
	if want := 24 - (3 + 1); m.vp.Height() != want {
		t.Errorf("对话区高度 = %d，期望 %d", m.vp.Height(), want)
	}
}

func TestValidateStatuslineReportsProblems(t *testing.T) {
	probs := ValidateStatusline([]StatusItem{
		{Name: "路径", Line: 1},
		{Name: "路径", Line: 1},
		{Name: "没有这一项", Line: 1},
		{Name: "状态", Line: 7},
		{Name: "工具", Line: 1, Color: "not a color!"},
		{},
	})
	joined := strings.Join(probs, "\n")
	for _, want := range []string{"出现了不止一次", "不是可用的项目名", "只能写 1-3", "无法识别", "没有 name"} {
		if !strings.Contains(joined, want) {
			t.Errorf("应报出 %q: %s", want, joined)
		}
	}
	// 合法的配置不该报任何问题。
	if probs := ValidateStatusline(DefaultStatusItems()); len(probs) > 0 {
		t.Errorf("默认布局应当合法: %v", probs)
	}
	for _, c := range []string{"0", "255", "75", "#fff", "#a1b2c3", "red", "bright-cyan"} {
		if !validStatusColor(c) {
			t.Errorf("%q 应是合法颜色", c)
		}
	}
	for _, c := range []string{"256", "-1", "#12", "not a color!"} {
		if validStatusColor(c) {
			t.Errorf("%q 不该是合法颜色", c)
		}
	}
}

func TestEffortAndSpeedHideThemselvesWithoutData(t *testing.T) {
	m := layoutModel(t, []StatusItem{
		{Name: "思考强度", Line: 1, Enabled: true},
		{Name: "速度", Line: 1, Enabled: true},
	})
	if lines := m.infoLines(); len(lines) != 0 {
		t.Fatalf("两项都没数据时应一行都不占: %q", lines)
	}
	info := testInfo
	info.Effort = "high"
	m = newSized(t, Options{Runner: scriptRunner(), Info: info, Statusline: []StatusItem{
		{Name: "思考强度", Line: 1, Enabled: true},
		{Name: "速度", Line: 1, Enabled: true},
	}}, 200, 24)
	if got := ansi.Strip(strings.Join(m.infoLines(), "\n")); !strings.Contains(got, "思考强度 high") || strings.Contains(got, "速度") {
		t.Errorf("有档位时才显示思考强度，速度仍无数据: %q", got)
	}
}

func TestFirstTokenLatencyIsShownNextToElapsed(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 200, 24)
	if line := infoText(m, 0); strings.Contains(line, "首字") {
		t.Errorf("没回答过时不显示首字延迟: %q", line)
	}
	clock := m.now()
	m.now = func() time.Time { return clock }
	m.started = clock
	clock = clock.Add(1200 * time.Millisecond)
	apply(m, event.MessageDelta{Text: "a"})
	if line := infoText(m, 0); !strings.Contains(line, "首字 1.2s") {
		t.Errorf("应显示从请求开始到首个增量的时间: %q", line)
	}
	// 同一次调用的后续增量不能把首字改写。
	clock = clock.Add(500 * time.Millisecond)
	apply(m, event.MessageDelta{Text: "b"})
	if line := infoText(m, 0); !strings.Contains(line, "首字 1.2s") || strings.Contains(line, "首字 1.7s") {
		t.Errorf("首字延迟只记第一次: %q", line)
	}
}

func TestCompactionCountShowsSuccessesOnly(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 200, 24)
	if line := infoText(m, 2); !strings.Contains(line, "压缩 0") {
		t.Errorf("一开始应是 0: %q", line)
	}
	apply(m, event.Compacted{Mode: event.CompactSummary, TokensBefore: 100, TokensAfter: 10})
	apply(m, event.Compacted{Mode: event.CompactSummary, TokensBefore: 100, TokensAfter: 100, Err: "失败"})
	if line := infoText(m, 2); !strings.Contains(line, "压缩 1") {
		t.Errorf("失败的压缩不该计数: %q", line)
	}
	// /new 要写会话文件，用带会话存储的 Model。
	n := sessionsModel(t, &fakeSessions{})
	n.compactions = 3
	n.in.SetValue("/new")
	_, cmd := n.Update(keyEnter)
	runCmd(n, cmd)
	if line := infoText(n, 2); !strings.Contains(line, "压缩 0") {
		t.Errorf("/new 之后应清零: %q", line)
	}
}
