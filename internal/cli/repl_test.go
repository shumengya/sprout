package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/event"
)

// echoRunner 记下每一轮的提问，并把它当作答案输出。
type echoRunner struct {
	mu   sync.Mutex
	got  []string
	hold func(ctx context.Context, in agent.Input) // 非 nil 时在回答前调用，用来模拟长时间生成
}

func (r *echoRunner) Run(ctx context.Context, in agent.Input, sink event.Sink) error {
	r.mu.Lock()
	r.got = append(r.got, in.Text)
	r.mu.Unlock()
	if r.hold != nil {
		r.hold(ctx, in)
	}
	if ctx.Err() != nil {
		sink.Emit(event.TurnFinished{Reason: event.ReasonCancelled})
		return ctx.Err()
	}
	sink.Emit(event.MessageDelta{Text: "答：" + in.Text})
	sink.Emit(event.TurnFinished{Reason: event.ReasonStop})
	return nil
}

func (r *echoRunner) asked() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.got...)
}

func plainInput(s string) agent.Input { return agent.Input{Text: s} }

func TestREPLAsksEachLineUntilExit(t *testing.T) {
	r := &echoRunner{}
	var out, errOut bytes.Buffer
	stdin := strings.NewReader("第一问\r\n\n多行\\\n第二行\n/exit\n不该被问到\n")
	code := REPL(r, plainInput("开场"), stdin, &out, &errOut, nil, plainInput)

	if code != ExitOK {
		t.Fatalf("退出码 = %d", code)
	}
	want := []string{"开场", "第一问", "多行\n第二行"}
	if got := r.asked(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("提问 = %q，期望 %q", got, want)
	}
	// 答案只在 stdout；提示符和说明在 stderr。
	if o := out.String(); !strings.Contains(o, "答：第一问\n") || strings.Contains(o, "> ") {
		t.Errorf("stdout = %q", o)
	}
	if e := errOut.String(); !strings.Contains(e, "> ") || !strings.Contains(e, "… ") || !strings.Contains(e, "/exit") {
		t.Errorf("stderr 应有提示符、续行提示和说明: %q", e)
	}
}

func TestREPLEndsAtEOF(t *testing.T) {
	r := &echoRunner{}
	code := REPL(r, agent.Input{}, strings.NewReader("只有一问"), io.Discard, io.Discard, nil, plainInput)
	if code != ExitOK || len(r.asked()) != 1 {
		t.Fatalf("stdin 结束应正常退出：code=%d 提问=%q", code, r.asked())
	}
}

// 生成中 Ctrl+C 只取消这一轮，之后照常读下一个提问；在提示符下 Ctrl+C 才退出。
func TestREPLInterruptCancelsTurnThenExitsAtPrompt(t *testing.T) {
	sig := make(chan os.Signal, 1)
	started := make(chan struct{}, 1)
	r := &echoRunner{hold: func(ctx context.Context, in agent.Input) {
		if in.Text != "慢问题" {
			return
		}
		started <- struct{}{}
		<-ctx.Done()
	}}
	pr, pw := io.Pipe()
	var errOut syncBuffer
	done := make(chan int, 1)
	go func() { done <- REPL(r, agent.Input{}, pr, io.Discard, &errOut, sig, plainInput) }()

	io.WriteString(pw, "慢问题\n")
	waitFor(t, started)
	sig <- os.Interrupt // 取消正在生成的这一轮
	io.WriteString(pw, "下一问\n")
	deadline := time.After(2 * time.Second)
	for len(r.asked()) < 2 {
		select {
		case <-deadline:
			t.Fatalf("取消后应继续读下一问，提问 = %q", r.asked())
		case <-time.After(10 * time.Millisecond):
		}
	}
	// 等它回到提示符再按 Ctrl+C。
	for !strings.HasSuffix(errOut.String(), "> ") {
		select {
		case <-deadline:
			t.Fatalf("没有回到提示符: %q", errOut.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	sig <- os.Interrupt
	select {
	case code := <-done:
		if code != ExitCancelled {
			t.Errorf("提示符下 Ctrl+C 应以 %d 退出，得到 %d", ExitCancelled, code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("提示符下 Ctrl+C 没有退出")
	}
	if !strings.Contains(errOut.String(), "已取消") {
		t.Errorf("被取消的那一轮应提示: %q", errOut.String())
	}
}

func waitFor(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("等待超时")
	}
}

// syncBuffer 是可并发读写的 bytes.Buffer：REPL 在自己的 goroutine 里写，测试同时读。
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
