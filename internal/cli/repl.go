package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/shumengya/sprout/internal/agent"
)

// REPLHelp 是交互模式开始时的说明，写在 stderr。
const REPLHelp = "交互模式：输入提问后回车发送，行尾写 \\ 续行；/exit 或 Ctrl+D 退出；生成中按 Ctrl+C 只取消当前回答。"

// REPL 实现 sprout run -i：一行一问，答案写 stdout，提示符与进度写 stderr，同一个会话里连续对话。
//
// first 非空时先问它。prepare 把输入的一行变成内核输入（附加正文里的图片路径等）。
// interrupts 收到信号时：生成中取消当前这一轮，回到提示符；在提示符下则退出（退出码 130）。
// 正常退出（/exit、/quit、stdin 结束）返回 0；某一轮失败不退出，接着等下一个提问。
func REPL(r Runner, first agent.Input, stdin io.Reader, stdout, stderr io.Writer,
	interrupts <-chan os.Signal, prepare func(string) agent.Input) int {
	lines := newLineReader(stdin)
	fmt.Fprintln(stderr, REPLHelp)
	if first.Text != "" || len(first.Images) > 0 {
		ask(r, first, stdout, stderr, interrupts)
	}
	for {
		text, ok, interrupted := readPrompt(lines, stderr, interrupts)
		switch {
		case interrupted:
			fmt.Fprintln(stderr)
			return ExitCancelled
		case !ok:
			fmt.Fprintln(stderr)
			return ExitOK
		}
		switch strings.TrimSpace(text) {
		case "":
			continue
		case "/exit", "/quit":
			return ExitOK
		}
		ask(r, prepare(strings.TrimSpace(text)), stdout, stderr, interrupts)
	}
}

// ask 跑一轮。Ctrl+C 取消这一轮的 ctx，等内核收尾（TurnFinished）后返回。
func ask(r Runner, in agent.Input, stdout, stderr io.Writer, interrupts <-chan os.Signal) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(ctx, r, in, stdout, stderr)
	}()
	for {
		select {
		case <-done:
			return
		case <-interrupts:
			cancel()
		}
	}
}

// readPrompt 显示提示符并读一个提问。行尾的 \ 表示续行，续行之间保留换行。
func readPrompt(lines *lineReader, stderr io.Writer, interrupts <-chan os.Signal) (text string, ok, interrupted bool) {
	var parts []string
	prompt := "> "
	for {
		fmt.Fprint(stderr, prompt)
		line, ok, interrupted := lines.next(interrupts)
		if interrupted || !ok {
			return "", false, interrupted
		}
		line = strings.TrimRight(line, "\r")
		if cont, found := strings.CutSuffix(line, `\`); found {
			parts = append(parts, cont)
			prompt = "… "
			continue
		}
		return strings.Join(append(parts, line), "\n"), true, false
	}
}

// lineReader 按需读行：只有在提示符下才去读 stdin。不提前读下一行，是因为 Windows 控制台里
// Ctrl+C 会打断进行中的读取并当作输入结束，生成中按 Ctrl+C 取消回答就会连带退出交互模式。
type lineReader struct {
	req chan struct{}
	out chan lineResult
}

type lineResult struct {
	line string
	ok   bool
}

func newLineReader(r io.Reader) *lineReader {
	lr := &lineReader{req: make(chan struct{}), out: make(chan lineResult)}
	go func() {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64<<10), 4<<20) // 允许粘贴一大段
		for range lr.req {
			ok := sc.Scan()
			lr.out <- lineResult{sc.Text(), ok}
			if !ok {
				return
			}
		}
	}()
	return lr
}

// next 读一行。等待期间收到中断就返回 interrupted；读的 goroutine 留在那里，进程随后退出。
func (lr *lineReader) next(interrupts <-chan os.Signal) (line string, ok, interrupted bool) {
	select {
	case lr.req <- struct{}{}:
	case <-interrupts:
		return "", false, true
	}
	select {
	case r := <-lr.out:
		return r.line, r.ok, false
	case <-interrupts:
		return "", false, true
	}
}
