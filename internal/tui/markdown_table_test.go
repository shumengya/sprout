package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestMarkdownTableColumnAlignment(t *testing.T) {
	src := "| 左 | 中 | 右 |\n|:---|:---:|---:|\n| a | b | c |\n| 长一点的左 | 长中 | 1234 |"
	lines := strings.Split(md(src, 80), "\n")
	if len(lines) != 6 {
		t.Fatalf("行数 = %d:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	// 右对齐列：数据行在右边框前齐平。
	if !strings.HasSuffix(lines[3], "   c │") || !strings.HasSuffix(lines[4], "1234 │") {
		t.Errorf("右对齐失败:\n%s", strings.Join(lines, "\n"))
	}
	// 居中列：b 在“长中”（4 列宽）里，空隙 3 格不能对称，左 1 右 2。
	if !strings.Contains(lines[3], "│  b   │") {
		t.Errorf("居中失败:\n%s", strings.Join(lines, "\n"))
	}
	assertTableGrid(t, lines)
}

func TestMarkdownTableHasFullBorder(t *testing.T) {
	lines := strings.Split(md("| a | b |\n|---|---|\n| 1 | 2 |", 40), "\n")
	want := []string{"┌───┬───┐", "│ a │ b │", "├───┼───┤", "│ 1 │ 2 │", "└───┴───┘"}
	if !slices.Equal(lines, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

func TestMarkdownTableRowSeparatorsOnlyWhenCellsWrap(t *testing.T) {
	seps := func(s string) int { return strings.Count(s, "├") }
	compact := md("| a | b |\n|---|---|\n| 1 | 2 |\n| 3 | 4 |\n| 5 | 6 |", 40)
	if n := seps(compact); n != 1 {
		t.Errorf("每行一条记录时只在表头下面画一条分隔线，得到 %d 条:\n%s", n, compact)
	}
	// 有单元格折成多行时，看不出哪几行属于同一条记录，所以每条记录之间都画分隔线。
	wrapped := md("| a | b |\n|---|---|\n| 1 | "+strings.Repeat("很长的说明", 6)+" |\n| 2 | 短 |\n| 3 | 短 |", 30)
	if n := seps(wrapped); n != 3 {
		t.Errorf("表头下 1 条加记录之间 2 条，得到 %d 条:\n%s", n, wrapped)
	}
	assertTableGrid(t, strings.Split(wrapped, "\n"))
}

func TestMarkdownTableShrinksAndWraps(t *testing.T) {
	src := "| 名 | 说明 |\n|---|---|\n| a | " + strings.Repeat("很长的说明", 12) + " |"
	got := md(src, 40)
	for i, l := range strings.Split(got, "\n") {
		if w := ansi.StringWidth(l); w > 40 {
			t.Errorf("第 %d 行宽 %d 超出 40: %q", i, w, l)
		}
	}
	// 折行会把词拆在两行之间，所以取出说明列的内容拼回去再比较。跳过上边框、表头、分隔线和其他边框行。
	var cell strings.Builder
	for _, l := range strings.Split(got, "\n")[3:] {
		if parts := strings.Split(l, "│"); len(parts) == 4 {
			cell.WriteString(strings.TrimSpace(parts[2]))
		}
	}
	if cell.String() != strings.Repeat("很长的说明", 12) {
		t.Errorf("折行不能丢字:\n%s", got)
	}
	if strings.Contains(got, "|--") {
		t.Errorf("不应退回原始 Markdown:\n%s", got)
	}
}

func TestMarkdownTableStacksWhenColumnsCannotFit(t *testing.T) {
	src := "| 甲 | 乙 | 丙 |\n|---|---|---|\n| 1 | 2 | 3 |"
	got := md(src, 10)
	if got != "甲 1\n乙 2\n丙 3" {
		t.Errorf("got %q", got)
	}
}

func TestMarkdownTableCellContent(t *testing.T) {
	src := "| a | b |\n|---|---|\n| **粗** | x\\|y |\n| 一<br>二 | z |"
	got := md(src, 60)
	for _, want := range []string{"粗", "x|y", "一", "二"} {
		if !strings.Contains(got, want) {
			t.Errorf("缺少 %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "**") || strings.Contains(got, "<br>") {
		t.Errorf("单元格里的标记应被渲染:\n%s", got)
	}
}

func TestMarkdownTableRaggedRows(t *testing.T) {
	// 数据行比表头少一格、多一格，都不能崩。
	got := md("| a | b |\n|---|---|\n| 1 |\n| 1 | 2 | 3 |", 60)
	if !strings.Contains(got, "1") || strings.Contains(got, "3") {
		t.Errorf("少的补空，多的丢弃:\n%s", got)
	}
}

func TestMarkdownTableHeaderOnly(t *testing.T) {
	if got := md("| a | b |\n|---|---|", 60); !strings.Contains(got, "a") || !strings.Contains(got, "b") {
		t.Errorf("got %q", got)
	}
	if got := md("| a | b |\n|---|---|", 3); !strings.Contains(got, "a") {
		t.Errorf("窄宽度只有表头也要显示: %q", got)
	}
}
