package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func md(src string, w int) string { return ansi.Strip(renderMarkdown(src, w)) }

func TestMarkdownHeadingAndInline(t *testing.T) {
	got := renderMarkdown("## 标题\n正文有 **粗体** 和 `代码`。", 60)
	plainText := ansi.Strip(got)
	if plainText != "标题\n正文有 粗体 和 代码。" {
		t.Fatalf("标记符号应被去掉: %q", plainText)
	}
	if !strings.Contains(got, "\x1b[") {
		t.Error("应带样式")
	}
}

func TestMarkdownUnclosedMarkersStayLiteral(t *testing.T) {
	// 流式输出中途语法还没写完，不能吞字符，也不能崩。
	for _, src := range []string{"半个 **粗体", "半个 `代码", "2 * 3 * 4", "a ** b"} {
		if got := md(src, 60); got != src {
			t.Errorf("md(%q) = %q，未闭合的标记应原样保留", src, got)
		}
	}
}

func TestMarkdownCodeFence(t *testing.T) {
	got := md("前文\n```go\nfunc main() {\n\treturn\n}\n```\n后文", 60)
	for _, want := range []string{"前文", "go", "  func main() {", "后文"} {
		if !strings.Contains(got, want) {
			t.Errorf("缺少 %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "```") {
		t.Errorf("围栏标记不应显示:\n%s", got)
	}
}

func TestMarkdownCodeIsNotInterpreted(t *testing.T) {
	got := md("```\n**不是粗体** `x`\n```", 60)
	if !strings.Contains(got, "**不是粗体** `x`") {
		t.Errorf("代码块内的标记应原样显示: %q", got)
	}
}

func TestMarkdownUnclosedFenceStreaming(t *testing.T) {
	got := md("```\nline1\nline2", 60)
	if !strings.Contains(got, "  line1") || !strings.Contains(got, "  line2") {
		t.Errorf("没闭合的围栏按代码处理: %q", got)
	}
}

func TestMarkdownLists(t *testing.T) {
	got := md("- 第一项\n* 第二项\n1. 编号一\n2) 编号二", 60)
	want := "• 第一项\n• 第二项\n1. 编号一\n2) 编号二"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestMarkdownBulletWrapsWithHangingIndent(t *testing.T) {
	got := md("- "+strings.Repeat("很长的内容", 10), 20)
	lines := strings.Split(got, "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "• ") || !strings.HasPrefix(lines[1], "  ") {
		t.Fatalf("续行应缩进对齐:\n%s", got)
	}
}

func TestMarkdownQuoteAndRule(t *testing.T) {
	got := md("> 引用内容\n---", 60)
	if !strings.HasPrefix(got, "> 引用内容") || !strings.Contains(got, "---") {
		t.Errorf("got %q", got)
	}
}

func TestMarkdownTableAlignsChineseColumns(t *testing.T) {
	src := "| 名称 | 大小 |\n|---|---|\n| main.go | 1 KB |\n| 说明文档 | 22 KB |"
	lines := strings.Split(md(src, 80), "\n")
	if len(lines) != 4 { // 表头、分隔线、两行数据
		t.Fatalf("行数 = %d:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	// 每一行的列分隔符必须在同一显示列上，中文占两列也要对齐。
	col := -1
	for _, l := range []string{lines[0], lines[2], lines[3]} {
		idx := ansi.StringWidth(l[:strings.Index(l, "│")])
		if col == -1 {
			col = idx
		} else if idx != col {
			t.Fatalf("列没有对齐 (%d vs %d):\n%s", idx, col, strings.Join(lines, "\n"))
		}
	}
}

func TestMarkdownTooWideTableFallsBackToText(t *testing.T) {
	src := "| " + strings.Repeat("宽", 30) + " | " + strings.Repeat("列", 30) + " |\n|---|---|\n| a | b |"
	got := md(src, 40)
	for i, l := range strings.Split(got, "\n") {
		if w := ansi.StringWidth(l); w > 40 {
			t.Errorf("第 %d 行宽 %d 超出 40: %q", i, w, l)
		}
	}
}

func TestMarkdownNotATable(t *testing.T) {
	// 含竖线但第二行不是分隔行，只是普通文字。
	if got := md("a | b\n只是文字", 60); got != "a | b\n只是文字" {
		t.Errorf("got %q", got)
	}
}

func TestMarkdownNeverExceedsWidth(t *testing.T) {
	src := "# " + strings.Repeat("标题", 30) + "\n" + strings.Repeat("很长的段落", 40) +
		"\n- " + strings.Repeat("列表", 40) + "\n> " + strings.Repeat("引用", 40) +
		"\n```\n" + strings.Repeat("代码", 60) + "\n```"
	for _, w := range []int{10, 20, 40, 80} {
		for i, l := range strings.Split(md(src, w), "\n") {
			if got := ansi.StringWidth(l); got > w {
				t.Errorf("w=%d 第 %d 行宽 %d 超宽: %q", w, i, got, l)
			}
		}
	}
}

func TestMarkdownEmpty(t *testing.T) {
	if got := renderMarkdown("", 40); got != "" {
		t.Errorf("got %q", got)
	}
}
