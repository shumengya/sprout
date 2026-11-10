package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	east "github.com/yuin/goldmark/extension/ast"
)

const (
	mdColGap  = 3 // 列之间的 " │ "
	mdColMin  = 4 // 列再窄就没法看了：一个全角字符加余量
	mdNoAlign = east.AlignNone
)

type mdCell struct {
	text  string // 行内渲染结果，可含换行（<br>）
	align east.Alignment
}

// table 渲染 GFM 表格：按显示宽度对齐（中文占两列）、遵守列对齐标记；
// 太宽时从最宽的列开始缩窄并在单元格内折行，窄到每列都放不下时改成逐行的“列名：内容”。
func (r *mdRenderer) table(n *east.Table, w int) []string {
	var rows [][]mdCell
	for row := n.FirstChild(); row != nil; row = row.NextSibling() {
		var cells []mdCell
		st := lipgloss.NewStyle()
		if _, isHeader := row.(*east.TableHeader); isHeader {
			st = mdBold
		}
		for c := row.FirstChild(); c != nil; c = c.NextSibling() {
			cell, _ := c.(*east.TableCell)
			if cell == nil {
				continue
			}
			r.in.trimNext = false
			cells = append(cells, mdCell{text: r.in.render(cell, st), align: cell.Alignment})
		}
		rows = append(rows, cells)
	}
	if len(rows) == 0 || len(rows[0]) == 0 {
		return nil
	}
	cols := len(rows[0])
	for i := range rows { // 补齐缺的单元格；多出的丢弃（GFM 规定）
		for len(rows[i]) < cols {
			rows[i] = append(rows[i], mdCell{})
		}
		rows[i] = rows[i][:cols]
	}

	widths := make([]int, cols)
	for _, row := range rows {
		for c, cell := range row {
			for _, l := range strings.Split(cell.text, "\n") {
				widths[c] = max(widths[c], ansi.StringWidth(l))
			}
		}
	}
	avail := w - mdColGap*(cols-1)
	if avail < mdColMin*cols {
		return r.tableStacked(rows, w)
	}
	mdShrink(widths, avail)

	var out []string
	for i, row := range rows {
		out = append(out, mdTableRow(row, widths, rows[0])...)
		if i == 0 {
			seps := make([]string, cols)
			for c := range seps {
				seps[c] = strings.Repeat("─", widths[c])
			}
			out = append(out, mdFaint.Render(strings.Join(seps, "─┼─")))
		}
	}
	return out
}

// mdShrink 反复把当前最宽的一列缩一格，直到总宽度不超过 avail。
func mdShrink(widths []int, avail int) {
	for {
		sum, widest := 0, 0
		for c, cw := range widths {
			sum += cw
			if cw > widths[widest] {
				widest = c
			}
		}
		if sum <= avail || widths[widest] <= mdColMin {
			return
		}
		widths[widest]--
	}
}

// mdTableRow 把一行渲染成若干物理行（单元格里折行或含 <br> 时一行会变高）。
func mdTableRow(row []mdCell, widths []int, header []mdCell) []string {
	wrapped := make([][]string, len(row))
	height := 1
	for c, cell := range row {
		for _, l := range strings.Split(cell.text, "\n") {
			wrapped[c] = append(wrapped[c], mdFlow(l, widths[c])...)
		}
		height = max(height, len(wrapped[c]))
	}
	sep := mdFaint.Render(" │ ")
	out := make([]string, height)
	for i := range out {
		parts := make([]string, len(row))
		for c := range row {
			line := ""
			if i < len(wrapped[c]) {
				line = wrapped[c][i]
			}
			align := row[c].align
			if align == mdNoAlign { // 单元格自己没写对齐时跟随表头那一列
				align = header[c].align
			}
			parts[c] = mdPad(line, widths[c], align)
		}
		out[i] = strings.TrimRight(strings.Join(parts, sep), " ")
	}
	return out
}

func mdPad(s string, w int, align east.Alignment) string {
	gap := max(w-ansi.StringWidth(s), 0)
	switch align {
	case east.AlignRight:
		return strings.Repeat(" ", gap) + s
	case east.AlignCenter:
		left := gap / 2
		return strings.Repeat(" ", left) + s + strings.Repeat(" ", gap-left)
	}
	return s + strings.Repeat(" ", gap)
}

// tableStacked 是窄终端下的退路：每一行数据展开成“列名 内容”的几行，行与行之间空一行。
func (r *mdRenderer) tableStacked(rows [][]mdCell, w int) []string {
	var out []string
	for _, row := range rows[1:] {
		if len(out) > 0 {
			out = append(out, "")
		}
		for c, cell := range row {
			head := mdBold.Render(ansi.Strip(rows[0][c].text)) + " "
			indent := min(ansi.StringWidth(head), w/2)
			body := mdFlow(strings.ReplaceAll(cell.text, "\n", " "), max(w-indent, 1))
			out = append(out, mdPrefix(body, ansi.Truncate(head, max(w-1, 1), ""), strings.Repeat(" ", indent))...)
		}
	}
	if len(out) == 0 { // 只有表头
		for _, cell := range rows[0] {
			out = append(out, mdFlow(cell.text, w)...)
		}
	}
	return out
}
