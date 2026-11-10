package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestMarkdownTableColumnAlignment(t *testing.T) {
	src := "| 左 | 中 | 右 |\n|:---|:---:|---:|\n| a | b | c |\n| 长一点的左 | 长中 | 1234 |"
	lines := strings.Split(md(src, 80), "\n")
	if len(lines) != 4 {
		t.Fatalf("行数 = %d:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	// 右对齐列：数据行的末尾必须齐平。
	if !strings.HasSuffix(lines[2], "   c") || !strings.HasSuffix(lines[3], "1234") {
		t.Errorf("右对齐失败:\n%s", strings.Join(lines, "\n"))
	}
	// 居中列：b 在“长中”（4 列宽）里，空隙 3 格不能对称，左 1 右 2。
	if !strings.Contains(lines[2], "│  b   │") {
		t.Errorf("居中失败:\n%s", strings.Join(lines, "\n"))
	}
	// 分隔线的 ┼ 要和数据行的 │ 在同一显示列。
	sep := strings.Index(lines[1], "┼")
	if ansi.StringWidth(lines[1][:sep]) != ansi.StringWidth(lines[2][:strings.Index(lines[2], "│")]) {
		t.Errorf("分隔线没对齐:\n%s", strings.Join(lines, "\n"))
	}
}

func TestMarkdownTableShrinksAndWraps(t *testing.T) {
	src := "| 名 | 说明 |\n|---|---|\n| a | " + strings.Repeat("很长的说明", 12) + " |"
	got := md(src, 40)
	for i, l := range strings.Split(got, "\n") {
		if w := ansi.StringWidth(l); w > 40 {
			t.Errorf("第 %d 行宽 %d 超出 40: %q", i, w, l)
		}
	}
	// 折行会把词拆在两行之间，所以取出说明列的内容拼回去再比较。
	var cell strings.Builder
	for _, l := range strings.Split(got, "\n")[2:] {
		_, after, _ := strings.Cut(l, "│")
		cell.WriteString(strings.TrimSpace(after))
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
