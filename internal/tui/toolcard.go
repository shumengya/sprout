package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/shumengya/sprout/internal/agent/event"
)

// 工具卡片的输出预览最多显示的行数。
const previewMax = 8

// commandOf 从工具参数里取出 command 用于展示；参数不是预期结构时退回原文。
// TUI 不依赖具体工具的 schema，这里只是个展示用的启发式。
func commandOf(args string) string {
	var a struct {
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(args), &a) == nil && strings.TrimSpace(a.Command) != "" {
		return a.Command
	}
	return args
}

func (e *entry) drawTool(w int) string {
	cmd := sanitize(e.command)
	first, rest, multi := strings.Cut(cmd, "\n")
	if multi && strings.TrimSpace(rest) != "" {
		first += " …"
	}
	head := styTool.Render(e.name) + "  " + ansi.Truncate(first, max(w-ansi.StringWidth(e.name)-2, 1), "…")

	var b strings.Builder
	b.WriteString(head)
	b.WriteString("\n  └ " + e.statusLine())
	if e.running {
		return b.String()
	}
	lines := outputLines(e.fin)
	for i, ln := range lines {
		if i == previewMax {
			b.WriteString("\n    " + styFaint.Render(fmt.Sprintf("… 还有 %d 行", len(lines)-previewMax)))
			break
		}
		b.WriteString("\n    " + styFaint.Render(ansi.Truncate(ln, max(w-4, 1), "…")))
	}
	return b.String()
}

func (e *entry) statusLine() string {
	f := e.fin
	switch {
	case e.running:
		return styFaint.Render("运行中…")
	case f.Cancelled:
		return styWarn.Render("已取消")
	case f.Err != "":
		msg, _, _ := strings.Cut(sanitize(f.Err), "\n")
		return styErr.Render("失败：" + msg)
	case f.TimedOut:
		return styWarn.Render("超时被终止，耗时 " + fmtDur(f.Duration))
	}
	s := fmt.Sprintf("%d 行，exit %d，%s", len(outputLines(f)), f.ExitCode, fmtDur(f.Duration))
	if f.Truncated {
		s += "，输出已截断"
	}
	if f.ExitCode != 0 {
		return styErr.Render(s)
	}
	return s
}

// outputLines 合并 stdout 与 stderr，作为预览与行数统计的来源。
func outputLines(f event.ToolFinished) []string {
	text := sanitize(f.Stdout)
	if f.Stderr != "" {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += sanitize(f.Stderr)
	}
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func fmtDur(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// fmtWait 把等待时间格式化成“3 秒”这样的中文，不足一秒按一秒算。
func fmtWait(d time.Duration) string {
	return fmt.Sprintf("%d 秒", max(int((d+time.Second-1)/time.Second), 1))
}

// describeCompacted 把压缩事件写成一句中文提示。压缩从不静默发生，用户总能看到历史被动过。
func describeCompacted(e event.Compacted) string {
	switch {
	case e.Err != "":
		return fmt.Sprintf("上下文接近上限（约 %d token），但生成摘要失败，历史保持原样继续：%s", e.TokensBefore, e.Err)
	case e.Mode == event.CompactPrune:
		return fmt.Sprintf("上下文接近上限，已裁剪 %d 条较早的工具输出（约 %d → %d token）", e.Dropped, e.TokensBefore, e.TokensAfter)
	}
	return fmt.Sprintf("上下文接近上限，已把更早的 %d 条消息总结成摘要（约 %d → %d token）", e.Dropped, e.TokensBefore, e.TokensAfter)
}
