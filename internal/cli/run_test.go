package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shumengya/sprout/internal/agent/event"
)

// fakeRunner 按脚本发事件。只用于测试。
type fakeRunner struct {
	events []event.Event
	err    error
}

func (f fakeRunner) Run(_ context.Context, _ string, sink event.Sink) error {
	for _, e := range f.events {
		sink.Emit(e)
	}
	return f.err
}

func run(ctx context.Context, r Runner) (code int, stdout, stderr string) {
	var o, e bytes.Buffer
	code = Run(ctx, r, "q", &o, &e)
	return code, o.String(), e.String()
}

func TestAnswerGoesToStdoutAndEndsWithNewline(t *testing.T) {
	code, out, errOut := run(context.Background(), fakeRunner{events: []event.Event{
		event.MessageDelta{Text: "你"}, event.MessageDelta{Text: "好"},
		event.TurnFinished{Reason: event.ReasonStop},
	}})
	if code != ExitOK || out != "你好\n" || errOut != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errOut)
	}
}

func TestToolProgressGoesToStderrOnly(t *testing.T) {
	code, out, errOut := run(context.Background(), fakeRunner{events: []event.Event{
		event.ToolRequested{Call: event.ToolCall{Name: "powershell", Arguments: `{"command":"Get-ChildItem"}`}},
		event.ToolFinished{Name: "powershell", ExitCode: 0, Duration: 1200 * time.Millisecond},
		event.MessageDelta{Text: "入口是 main.go"},
		event.TurnFinished{Reason: event.ReasonStop},
	}})
	if code != ExitOK || out != "入口是 main.go\n" {
		t.Fatalf("stdout 只应有答案: code=%d stdout=%q", code, out)
	}
	for _, want := range []string{"[powershell] Get-ChildItem", "exit 0，1.2s"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr 缺少 %q: %q", want, errOut)
		}
	}
}

func TestToolFailureSummaries(t *testing.T) {
	cases := map[string]event.ToolFinished{
		"已取消":        {Cancelled: true},
		"失败: 起不来":    {Err: "起不来\n第二行"},
		"超时被终止":      {TimedOut: true, Duration: 2 * time.Second},
		"输出已截断":      {Truncated: true},
		"exit 3，5ms": {ExitCode: 3, Duration: 5 * time.Millisecond},
	}
	for want, fin := range cases {
		if got := toolSummary(fin); !strings.Contains(got, want) {
			t.Errorf("toolSummary(%+v) = %q，应含 %q", fin, got, want)
		}
	}
	if strings.Contains(toolSummary(event.ToolFinished{Err: "起不来\n第二行"}), "第二行") {
		t.Error("多行错误只应显示首行")
	}
}

func TestStderrLinesDoNotGlueOntoUnfinishedAnswer(t *testing.T) {
	// 答案没有以换行结束时遇到 stderr 输出，要先补换行，否则同在终端里会粘成一行。
	var o, e bytes.Buffer
	p := &printer{out: &o, err: &e, atLineStart: true}
	p.Emit(event.MessageDelta{Text: "半句话"})
	p.Emit(event.Error{Message: "出错了"})
	if o.String() != "半句话\n" {
		t.Fatalf("stdout = %q", o.String())
	}
}

func TestExitCodes(t *testing.T) {
	failed := errors.New("boom")
	if code, _, errOut := run(context.Background(), fakeRunner{
		events: []event.Event{event.Error{Message: "认证失败"}, event.TurnFinished{Reason: event.ReasonError}}, err: failed,
	}); code != ExitError || !strings.Contains(errOut, "错误: 认证失败") {
		t.Errorf("失败: code=%d stderr=%q", code, errOut)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if code, _, errOut := run(ctx, fakeRunner{
		events: []event.Event{event.TurnFinished{Reason: event.ReasonCancelled}}, err: context.Canceled,
	}); code != ExitCancelled || !strings.Contains(errOut, "已取消") {
		t.Errorf("取消: code=%d stderr=%q", code, errOut)
	}
}

func TestLengthTruncationIsReportedButNotFailure(t *testing.T) {
	code, _, errOut := run(context.Background(), fakeRunner{events: []event.Event{
		event.MessageDelta{Text: "半"}, event.TurnFinished{Reason: event.ReasonLength},
	}})
	if code != ExitOK || !strings.Contains(errOut, "长度上限") {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
}

func TestControlSequencesAreStripped(t *testing.T) {
	code, out, errOut := run(context.Background(), fakeRunner{events: []event.Event{
		event.MessageDelta{Text: "正常\x1b[2J\x07文本"},
		event.ToolRequested{Call: event.ToolCall{Name: "bash", Arguments: `{"command":"echo \u001b]0;pwn\u0007"}`}},
		event.Error{Message: "坏\x1b[31m消息"},
	}})
	_ = code
	for _, s := range []string{out, errOut} {
		if strings.ContainsRune(s, 0x1b) || strings.ContainsRune(s, 0x07) {
			t.Fatalf("输出里不应有控制字符: %q", s)
		}
	}
	if !strings.Contains(out, "正常") || !strings.Contains(out, "文本") {
		t.Errorf("正常文本应保留: %q", out)
	}
}

func TestCleanKeepsNewlinesAndTabs(t *testing.T) {
	if got := clean("a\tb\nc\rd\x00e"); got != "a\tb\ncde" {
		t.Fatalf("clean = %q", got)
	}
}

func TestEmptyAnswerPrintsNothingOnStdout(t *testing.T) {
	_, out, _ := run(context.Background(), fakeRunner{events: []event.Event{event.TurnFinished{Reason: event.ReasonStop}}})
	if out != "" {
		t.Fatalf("stdout = %q", out)
	}
}
