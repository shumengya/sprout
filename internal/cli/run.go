// Package cli 实现非交互的单次运行：sprout run "..."。
//
// 助手文本流式写 stdout；工具进度、错误、提示写 stderr，
// 这样脚本里 `sprout run ... > answer.txt` 只会捕获答案本身。
package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/shumengya/sprout/internal/agent/event"
)

// 进程退出码。
const (
	ExitOK        = 0
	ExitError     = 1
	ExitCancelled = 130 // 约定：被 SIGINT 终止
)

// Runner 是 CLI 眼中的内核，*agent.Agent 满足它。
type Runner interface {
	Run(ctx context.Context, prompt string, sink event.Sink) error
}

// Run 执行一次提问并返回退出码。所有面向用户的输出（含错误）都已在这里打印，
// 调用方不需要再打印返回值对应的错误。
func Run(ctx context.Context, r Runner, prompt string, stdout, stderr io.Writer) int {
	p := &printer{out: stdout, err: stderr, atLineStart: true}
	err := r.Run(ctx, prompt, p)
	p.endOutput()
	switch {
	case err == nil:
		return ExitOK
	case ctx.Err() != nil:
		return ExitCancelled
	}
	return ExitError
}

// printer 把事件翻译成文本输出。
type printer struct {
	out, err    io.Writer
	atLineStart bool // stdout 当前是否在行首
}

func (p *printer) Emit(e event.Event) {
	switch e := e.(type) {
	case event.MessageDelta:
		p.write(clean(e.Text))
	case event.ToolRequested:
		p.status("⚙ %s  %s", e.Call.Name, firstLine(clean(commandOf(e.Call.Arguments))))
	case event.ToolFinished:
		p.status("  └ %s", toolSummary(e))
	case event.Error:
		p.status("错误: %s", firstLine(clean(e.Message)))
	case event.TurnFinished:
		switch e.Reason {
		case event.ReasonCancelled:
			p.status("已取消。")
		case event.ReasonLength:
			p.status("提示: 模型输出被长度上限截断，回答可能不完整。")
		}
	}
}

func (p *printer) write(s string) {
	if s == "" {
		return
	}
	io.WriteString(p.out, s)
	p.atLineStart = strings.HasSuffix(s, "\n")
}

// endOutput 保证 stdout 以换行结束，避免 shell 提示符接在答案末尾。
func (p *printer) endOutput() {
	if !p.atLineStart {
		p.write("\n")
	}
}

// status 写一行到 stderr。先补齐 stdout 的换行，否则两者同在终端时会粘在同一行。
func (p *printer) status(format string, args ...any) {
	p.endOutput()
	fmt.Fprintf(p.err, format+"\n", args...)
}

func toolSummary(e event.ToolFinished) string {
	switch {
	case e.Cancelled:
		return "已取消"
	case e.Err != "":
		return "失败: " + firstLine(clean(e.Err))
	case e.TimedOut:
		return "超时被终止，耗时 " + dur(e.Duration)
	}
	s := fmt.Sprintf("exit %d，%s", e.ExitCode, dur(e.Duration))
	if e.Truncated {
		s += "，输出已截断"
	}
	return s
}

func dur(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}
