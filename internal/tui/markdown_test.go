package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// mdRaw 返回带样式的渲染结果。渲染器出错直接 panic 让测试失败并带上堆栈：
// 生产路径的 renderMarkdown 会退回纯文本，用它测试会把 bug 藏起来。
func mdRaw(src string, w int) string {
	out, err := renderMarkdownE(src, w)
	if err != nil {
		panic(err)
	}
	return out
}

// md 返回去掉样式后的渲染结果。
func md(src string, w int) string { return ansi.Strip(mdRaw(src, w)) }

func TestMarkdownHeadingAndInline(t *testing.T) {
	got := mdRaw("## 标题\n正文有 **粗体** 和 `代码`。", 60)
	if plainText := ansi.Strip(got); plainText != "标题\n\n正文有 粗体 和 代码。" {
		t.Fatalf("标记符号应被去掉，块之间空一行: %q", plainText)
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
	if got != "  line1\n  line2" {
		t.Errorf("没闭合的围栏按代码处理: %q", got)
	}
}

func TestMarkdownFenceInfoStringKeepsOnlyLanguage(t *testing.T) {
	if got := md("```go title=main.go\nx := 1\n```", 60); got != "go\n  x := 1" {
		t.Errorf("got %q", got)
	}
}

func TestMarkdownBulletsAndNumbers(t *testing.T) {
	if got := md("- 第一项\n- 第二项", 60); got != "• 第一项\n• 第二项" {
		t.Errorf("bullets: %q", got)
	}
	// 换了标记符号就是另一个列表（CommonMark 规定），两个列表之间空一行。
	if got := md("- 第一项\n* 第二项", 60); got != "• 第一项\n\n• 第二项" {
		t.Errorf("mixed markers: %q", got)
	}
	if got := md("1. 编号一\n2. 编号二", 60); got != "1. 编号一\n2. 编号二" {
		t.Errorf("numbers: %q", got)
	}
	if got := md("3) 从三开始\n4) 接着", 60); got != "3) 从三开始\n4) 接着" {
		t.Errorf("start/marker: %q", got)
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
	got := md("> 引用内容\n\n---", 60)
	if !strings.HasPrefix(got, "│ 引用内容") || !strings.Contains(got, "────") {
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

func TestMarkdownNotATable(t *testing.T) {
	// 含竖线但第二行不是分隔行，只是普通文字。
	if got := md("a | b\n只是文字", 60); got != "a | b\n只是文字" {
		t.Errorf("got %q", got)
	}
}

func TestMarkdownEmpty(t *testing.T) {
	for _, src := range []string{"", "\n\n", "  \n \n"} {
		if got := md(src, 40); got != "" {
			t.Errorf("md(%q) = %q", src, got)
		}
	}
}
