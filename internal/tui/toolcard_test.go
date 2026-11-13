package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shumengya/sprout/internal/agent/event"
)

func requested(id, name, args string) event.ToolRequested {
	return event.ToolRequested{Call: event.ToolCall{ID: id, Name: name, Arguments: args}}
}

func TestToolRunningThenFinished(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 30)
	begin(m, "看目录")
	apply(m, requested("c1", "powershell", `{"command":"Get-ChildItem"}`))
	mustContain(t, plain(m), "powershell", "Get-ChildItem", "运行中", "执行 powershell")

	apply(m, event.ToolFinished{ID: "c1", Name: "powershell", Command: "Get-ChildItem",
		Stdout: "a.go\nb.go\n", ExitCode: 0, Duration: 1500 * time.Millisecond})
	s := plain(m)
	mustContain(t, s, "2 行，exit 0，1.5s", "a.go", "b.go")
	mustNotContain(t, s, "运行中")
	if n := m.runningTools(); len(n) != 0 {
		t.Errorf("工具结束后状态行不应再显示工具名: %v", n)
	}
}

func TestToolPreviewIsTruncatedToEightLines(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 40)
	begin(m, "q")
	var out strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&out, "line%02d\n", i)
	}
	apply(m, requested("c", "bash", `{"command":"seq"}`),
		event.ToolFinished{ID: "c", Name: "bash", Stdout: out.String()})
	s := plain(m)
	mustContain(t, s, "20 行", "line01", "line08", "… 还有 12 行")
	mustNotContain(t, s, "line09", "line20")
}

func TestToolFailureStates(t *testing.T) {
	cases := []struct {
		name string
		fin  event.ToolFinished
		want []string
	}{
		{"非零退出码", event.ToolFinished{ExitCode: 2, Stderr: "boom\n"}, []string{"exit 2", "boom"}},
		{"超时", event.ToolFinished{TimedOut: true, ExitCode: -1, Duration: 3 * time.Second}, []string{"超时被终止", "3.0s"}},
		{"被取消", event.ToolFinished{Cancelled: true, Err: "已取消"}, []string{"已取消"}},
		{"工具错误", event.ToolFinished{Err: "无法启动 bash: 不存在\n多余的第二行"}, []string{"失败：无法启动 bash: 不存在"}},
		{"输出被截断", event.ToolFinished{Stdout: "x", Truncated: true}, []string{"输出已截断"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := newSized(t, Options{Runner: scriptRunner()}, 100, 30)
			begin(m, "q")
			c.fin.ID, c.fin.Name = "c", "bash"
			apply(m, requested("c", "bash", `{"command":"x"}`), c.fin)
			mustContain(t, plain(m), c.want...)
			mustNotContain(t, plain(m), "多余的第二行")
		})
	}
}

func TestSkippedToolWithoutRequestStillShown(t *testing.T) {
	// 取消时尚未执行的调用只有 ToolFinished，没有 ToolRequested。
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 30)
	begin(m, "q")
	apply(m, event.ToolFinished{ID: "never", Name: "bash", Cancelled: true, Err: "未执行"})
	mustContain(t, plain(m), "bash", "已取消")
}

func TestToolOutputIsSanitized(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 30)
	begin(m, "q")
	apply(m, requested("c", "bash", `{"command":"ls"}`),
		event.ToolFinished{ID: "c", Name: "bash", Stdout: "\x1b[31m红色\x1b[0m\t制表\r\n\x07响铃\n"})
	s := m.View().Content
	if strings.Contains(s, "\x1b[31m") || strings.Contains(s, "\x07") || strings.Contains(s, "\t") || strings.Contains(s, "\r") {
		t.Fatalf("输出里的控制序列没有被清除: %q", s)
	}
	mustContain(t, plain(m), "红色", "制表", "响铃")
}

func TestMultilineCommandCollapsedToOneLine(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 30)
	begin(m, "q")
	apply(m, requested("c", "bash", `{"command":"echo one\necho two"}`))
	s := plain(m)
	mustContain(t, s, "bash(echo one echo two)")
}

func TestToolCallShownAsFunctionCall(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 30)
	begin(m, "q")
	apply(m, requested("r", "read", `{"path":"a/b.go","offset":10}`))
	mustContain(t, plain(m), "read(a/b.go)")
}

func TestClickExpandsToolCall(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 40)
	begin(m, "q")
	var out strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&out, "line%02d\n", i)
	}
	apply(m, requested("c", "bash", `{"command":"seq"}`),
		event.ToolFinished{ID: "c", Name: "bash", Stdout: out.String()})
	mustContain(t, plain(m), "… 还有 12 行，点击展开")

	// 点击工具卡片（它是最后一个条目）：参数与输出都展开到完整内容。
	m.toggleAt(m.spans[len(m.spans)-1].start)
	s := plain(m)
	mustContain(t, s, "line01", "line20")
	mustNotContain(t, s, "点击展开")
}

func TestExpandedMultilineCommandIsFullyShown(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 30)
	begin(m, "q")
	apply(m, requested("c", "bash", `{"command":"echo one\necho two"}`),
		event.ToolFinished{ID: "c", Name: "bash", Stdout: "ok\n"})
	m.toggleAt(m.spans[len(m.spans)-1].start)
	s := plain(m)
	mustContain(t, s, "bash(", "  echo one", "  echo two", ")")
}

func TestCommandOfFallsBackToRawArgs(t *testing.T) {
	cases := map[string]string{
		`{"command":"ls -la"}`: "ls -la",
		`{"cmd":"x"}`:          `{"cmd":"x"}`,
		`not json`:             "not json",
		`{"command":"  "}`:     `{"command":"  "}`,
	}
	for in, want := range cases {
		if got := commandOf(in); got != want {
			t.Errorf("commandOf(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestLongChineseOutputNeverOverflowsWidth(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 40, 30)
	begin(m, strings.Repeat("很长的提问", 20))
	apply(m, event.MessageDelta{Text: strings.Repeat("很长的回答", 30)},
		requested("c", "bash", `{"command":"`+strings.Repeat("长命令", 30)+`"}`),
		event.ToolFinished{ID: "c", Name: "bash", Stdout: strings.Repeat("长输出", 40)})
	for i, l := range strings.Split(plain(m), "\n") {
		if w := len([]rune(l)); w > 40 { // 中文一个字符占两列，按字符数估算上界
			t.Errorf("第 %d 行 %d 个字符，疑似超出终端宽度: %q", i, w, l)
		}
	}
}

func TestParallelToolsShownTogether(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 40)
	begin(m, "q")
	apply(m, requested("1", "bash", `{"command":"ls"}`), requested("2", "powershell", `{"command":"dir"}`))
	mustContain(t, plain(m), "并行执行 2 个工具")

	// 先结束一个：另一个还在跑，状态行要变成它的名字，而不是被清空。
	apply(m, event.ToolFinished{ID: "2", Name: "powershell", Command: "dir", Stdout: "x\n"})
	if names := m.runningTools(); len(names) != 1 || names[0] != "bash" {
		t.Fatalf("仍在运行的应只有 bash: %v", names)
	}
	mustContain(t, plain(m), "执行 bash")

	apply(m, event.ToolFinished{ID: "1", Name: "bash", Command: "ls", Stdout: "y\n"})
	if len(m.runningTools()) != 0 {
		t.Fatal("全部结束后不应再有运行中的工具")
	}
	mustNotContain(t, plain(m), "运行中", "并行执行")
}

func TestRunningToolsAreSweptWhenTurnEnds(t *testing.T) {
	// 轮次结束（比如通道提前关闭）时，没收到 ToolFinished 的卡片不能一直显示“运行中…”。
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 30)
	begin(m, "q")
	apply(m, requested("1", "bash", `{"command":"sleep 99"}`), event.TurnFinished{Reason: event.ReasonCancelled})
	if len(m.runningTools()) != 0 {
		t.Fatal("轮次结束后不应还有运行中的工具")
	}
	s := plain(m)
	mustContain(t, s, "已取消")
	mustNotContain(t, s, "运行中…")
}
