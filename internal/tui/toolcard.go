package tui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/shumengya/sprout/internal/agent/event"
)

// 工具卡片折叠时，输出预览最多显示的行数。
const previewMax = 8

// commandOf 从工具参数里取出用于展示的一行：
// shell 工具取 command，文件工具取 path（read / write 没有 command 字段），agent 工具取「类型: 标题」，都没有时退回原文。
// TUI 不依赖具体工具的 schema，这里只是个展示用的启发式。
func commandOf(args string) string {
	var a struct {
		Command     string `json:"command"`
		Path        string `json:"path"`
		Description string `json:"description"`
		Agent       string `json:"agent"`
	}
	if json.Unmarshal([]byte(args), &a) == nil {
		if strings.TrimSpace(a.Command) != "" {
			return a.Command
		}
		if strings.TrimSpace(a.Path) != "" {
			return a.Path
		}
		if strings.TrimSpace(a.Description) != "" {
			if strings.TrimSpace(a.Agent) != "" {
				return a.Agent + ": " + a.Description
			}
			return a.Description
		}
	}
	return args
}

// drawTool 把工具调用画成一行函数调用 name(args)，参数放括号里；点击条目展开或收起。
// 展开时展示完整参数（多行命令按行铺开）和全部输出；收起时参数压成一行并截断，输出只留前几行。
func (e *entry) drawTool(w int) string {
	var b strings.Builder
	b.WriteString(e.callHead(w))
	b.WriteString("\n  └ " + e.statusLine())
	if e.sub != nil {
		b.WriteString(e.drawSub(w))
	}
	if e.running {
		return b.String()
	}
	lines := outputLines(e.fin)
	limit := len(lines)
	if !e.open {
		limit = previewMax
	}
	for i := 0; i < min(limit, len(lines)); i++ {
		b.WriteString("\n    " + styFaint.Render(ansi.Truncate(lines[i], max(w-4, 1), "…")))
	}
	if len(lines) > limit {
		b.WriteString("\n    " + styFaint.Render(fmt.Sprintf("… 还有 %d 行，点击展开", len(lines)-limit)))
	}
	return b.String()
}

// callHead 是 name(args) 这一行。技能触发的标记同样放在前面。
func (e *entry) callHead(w int) string {
	// 触发技能时在最前面标出来，和普通工具调用一眼区分开。
	lead := ""
	leadW := 0
	if e.skill != "" {
		lead = stySkill.Render("[技能] ") + stySkill.Render(e.skill) + "  "
		leadW = ansi.StringWidth(lead)
	}
	name := styTool.Render(e.name)
	nameW := ansi.StringWidth(e.name)
	arg := sanitize(e.command)

	if !e.open || !strings.Contains(arg, "\n") {
		// 折叠时一律压成一行；展开时单行命令也保持一行，超宽就折行，不丢内容。
		flat := strings.Join(strings.Fields(arg), " ")
		room := max(w-leadW-nameW-3, 1) // 留出括号和省略号的位置
		if e.open {
			return lead + name + "(" + styFaint.Render(wrap(flat, room)) + ")"
		}
		return lead + name + "(" + styFaint.Render(ansi.Truncate(flat, room, "…")) + ")"
	}
	// 展开的多行命令：括号单独占行，每条命令一行缩进后折行，收尾再补一个右括号。
	parts := []string{lead + name + "("}
	for _, ln := range strings.Split(strings.TrimRight(arg, "\n"), "\n") {
		for _, l := range strings.Split(wrap(ln, max(w-4, 1)), "\n") {
			parts = append(parts, styFaint.Render("  "+l))
		}
	}
	parts = append(parts, styFaint.Render(")"))
	return strings.Join(parts, "\n")
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

// fmtTokens 把 token 数缩写成 950、2.9k、12k、1.5M 这样，四舍五入，宽度最多 4 列左右。
// 只用整数运算：浮点数下 1050/1000 不一定恰好是 1.05，四舍五入会偶尔差一位。
func fmtTokens(n int) string {
	switch {
	case n < 0:
		return "-" + fmtTokens(-n)
	case n < 1000:
		return strconv.Itoa(n)
	case n < 1_000_000:
		return scaled(n, 1000, "k", "M")
	}
	return scaled(n, 1_000_000, "M", "")
}

// scaled 把 n 按 unit 缩写：不到 10 个单位保留一位小数（去掉 .0），否则取整；
// 进位到 1000 个单位时换成下一级（999.96k 显示成 1M 而不是 1000k）。
func scaled(n, unit int, suffix, next string) string {
	tenths := (n*10 + unit/2) / unit // 以 0.1 个单位计，四舍五入
	if tenths < 100 {
		if tenths%10 == 0 {
			return strconv.Itoa(tenths/10) + suffix
		}
		return fmt.Sprintf("%d.%d%s", tenths/10, tenths%10, suffix)
	}
	whole := (n + unit/2) / unit
	if whole >= 1000 && next != "" {
		return fmtTokens(whole * unit)
	}
	return strconv.Itoa(whole) + suffix
}

// describeCompacted 把压缩事件写成一句中文提示。压缩从不静默发生，用户总能看到历史被动过。
func describeCompacted(e event.Compacted) string {
	switch {
	case e.Manual && e.Err != "":
		return fmt.Sprintf("压缩上下文失败，对话保持原样：%s", e.Err)
	case e.Manual:
		return fmt.Sprintf("已把前面 %d 条消息总结成摘要（约 %s → %s token）。上面的内容仍可翻看，之后的回答基于这份摘要。",
			e.Dropped, fmtTokens(e.TokensBefore), fmtTokens(e.TokensAfter))
	case e.Err != "":
		return fmt.Sprintf("上下文接近上限（约 %s token），但生成摘要失败，历史保持原样继续：%s", fmtTokens(e.TokensBefore), e.Err)
	case e.Mode == event.CompactPrune:
		return fmt.Sprintf("上下文接近上限，已裁剪 %d 条较早的工具输出（约 %s → %s token）",
			e.Dropped, fmtTokens(e.TokensBefore), fmtTokens(e.TokensAfter))
	}
	return fmt.Sprintf("上下文接近上限，已把更早的 %d 条消息总结成摘要（约 %s → %s token）",
		e.Dropped, fmtTokens(e.TokensBefore), fmtTokens(e.TokensAfter))
}
