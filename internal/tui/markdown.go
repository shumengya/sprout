package tui

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// 轻量 Markdown 渲染：只覆盖模型回答里最常见的写法，不追求完整的 CommonMark。
// 流式输出时每来一段增量都会整段重渲，所以必须容忍没写完的语法（比如还没闭合的代码围栏）。

var (
	styCode    = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styBold    = lipgloss.NewStyle().Bold(true)
	styHeading = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3"))
)

var (
	reBullet   = regexp.MustCompile(`^[-*+] +(.*)$`)
	reNumbered = regexp.MustCompile(`^(\d{1,3}[.)]) +(.*)$`)
	reHeading  = regexp.MustCompile(`^#{1,6} +(.*)$`)
	reRule     = regexp.MustCompile(`^(-{3,}|\*{3,}|_{3,})$`)
	reTableSep = regexp.MustCompile(`^\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?$`)
)

// renderMarkdown 把 src 渲染成不超过 w 列的终端文本。src 应已经过 sanitize。
func renderMarkdown(src string, w int) string {
	w = max(w, 1)
	lines := strings.Split(src, "\n")
	var out []string
	inCode := false
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if lang := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "`")); !inCode && lang != "" {
				out = append(out, styFaint.Render(lang))
			}
			inCode = !inCode
			continue
		}
		if inCode {
			// 代码不重排，只在超宽时硬折行，保留缩进。
			for _, l := range strings.Split(ansi.Hardwrap(line, max(w-2, 1), true), "\n") {
				out = append(out, "  "+styCode.Render(l))
			}
			continue
		}
		if tbl, n := renderTable(lines[i:], w); n > 0 {
			out = append(out, tbl...)
			i += n - 1
			continue
		}
		out = append(out, renderLine(line, w))
	}
	return strings.Join(out, "\n")
}

// renderLine 渲染单行（非代码、非表格）：标题、列表、引用、分隔线或普通段落。
func renderLine(line string, w int) string {
	trimmed := strings.TrimSpace(line)
	indent := len(line) - len(strings.TrimLeft(line, " "))
	pad := strings.Repeat(" ", min(indent, w/2))

	switch {
	case trimmed == "":
		return ""
	case reRule.MatchString(trimmed):
		return styFaint.Render(strings.Repeat("-", min(w, 40)))
	}
	if m := reHeading.FindStringSubmatch(trimmed); m != nil {
		return wrap(styHeading.Render(strings.TrimSpace(m[1])), w)
	}
	if m := reBullet.FindStringSubmatch(trimmed); m != nil {
		return hang(pad+"• ", len(pad)+2, wrap(inline(m[1]), w-len(pad)-2))
	}
	if m := reNumbered.FindStringSubmatch(trimmed); m != nil {
		prefix := pad + m[1] + " "
		return hang(prefix, len(prefix), wrap(inline(m[2]), w-len(prefix)))
	}
	if strings.HasPrefix(trimmed, ">") {
		body := strings.TrimSpace(strings.TrimLeft(trimmed, ">"))
		return styFaint.Render(hang("> ", 2, wrap(body, w-2)))
	}
	return wrap(inline(trimmed), w)
}

// inline 处理行内的 `代码` 和 **粗体**。没有闭合的标记原样保留，不猜测。
func inline(s string) string {
	if !strings.ContainsAny(s, "`*") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		switch {
		case s[i] == '`':
			if j := strings.IndexByte(s[i+1:], '`'); j > 0 {
				b.WriteString(styCode.Render(s[i+1 : i+1+j]))
				i += j + 2
				continue
			}
		case strings.HasPrefix(s[i:], "**"):
			if j := strings.Index(s[i+2:], "**"); j > 0 {
				b.WriteString(styBold.Render(s[i+2 : i+2+j]))
				i += j + 4
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		b.WriteRune(r)
		i += size
	}
	return b.String()
}

// renderTable 尝试把 lines 开头的内容渲染成对齐的表格，返回渲染结果和消耗的行数；
// 不是表格（或太宽放不下）时返回 0，调用方按普通文本处理。
func renderTable(lines []string, w int) ([]string, int) {
	if len(lines) < 2 || !strings.Contains(lines[0], "|") || !reTableSep.MatchString(strings.TrimSpace(lines[1])) ||
		!strings.Contains(lines[1], "-") {
		return nil, 0
	}
	rows := [][]string{splitRow(lines[0])}
	n := 2
	for ; n < len(lines) && strings.Contains(lines[n], "|"); n++ {
		rows = append(rows, splitRow(lines[n]))
	}
	cols := len(rows[0])
	widths := make([]int, cols)
	cells := make([][]string, len(rows))
	for r, row := range rows {
		cells[r] = make([]string, cols)
		for c := 0; c < cols; c++ {
			if c < len(row) {
				cells[r][c] = inline(row[c])
			}
			widths[c] = max(widths[c], ansi.StringWidth(cells[r][c]))
		}
	}
	total := 0
	for _, cw := range widths {
		total += cw + 3
	}
	if total > w { // 放不下就不画表格，退回普通文本，至少不会撑破布局
		return nil, 0
	}

	var out []string
	for r, row := range cells {
		parts := make([]string, cols)
		for c, cell := range row {
			if r == 0 {
				cell = styBold.Render(ansi.Strip(cell))
			}
			parts[c] = cell + strings.Repeat(" ", widths[c]-ansi.StringWidth(cell))
		}
		out = append(out, strings.Join(parts, styFaint.Render(" │ ")))
		if r == 0 {
			seps := make([]string, cols)
			for c := range seps {
				seps[c] = strings.Repeat("─", widths[c])
			}
			out = append(out, styFaint.Render(strings.Join(seps, "─┼─")))
		}
	}
	return out, n
}

func splitRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}
