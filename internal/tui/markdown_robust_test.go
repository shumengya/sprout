package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

const richDoc = "# 标题 " + "很长的标题文字很长的标题文字很长的标题文字" + "\n\n正文有 **粗体**、*斜体*、~~删除线~~、`行内代码`、[链接](https://example.com/a/very/long/path)。\n" +
	"第二行 https://example.com/a/very/long/path/that/never/ends/and/never/breaks。\n\n" +
	"> 引用 " + "很长的引用文字很长的引用文字很长的引用文字" + "\n> > 嵌套\n\n" +
	"- 列表 " + "很长的列表文字很长的列表文字很长的列表文字" + "\n  - 嵌套 [ ] 项\n    1. 编号\n- [x] 任务\n\n" +
	"```go\n// 注释\nfunc main() {\n\tfmt.Println(\"你好，世界\", 42)\n}\n```\n\n" +
	"| 名称 | 数量 | 说明 |\n|:---|---:|:---:|\n| main.go | 12 | 入口 |\n| 说明文档 | 3456 | 很长很长的说明文字需要换行 |\n\n" +
	"---\n\n![图](http://x/y.png) <kbd>Ctrl</kbd> &amp; 结尾\n"

func TestMarkdownNeverExceedsWidth(t *testing.T) {
	for _, w := range []int{1, 2, 3, 5, 8, 10, 20, 36, 80} {
		for i, l := range strings.Split(md(richDoc, w), "\n") {
			if got := ansi.StringWidth(l); got > w {
				t.Errorf("w=%d 第 %d 行宽 %d 超宽: %q", w, i, got, l)
			}
		}
	}
}

func TestMarkdownLinesAreSelfContained(t *testing.T) {
	// 每一行结束时都不能有没关的样式，否则前缀、相邻行会被染色。
	for _, w := range []int{10, 20, 36, 80} {
		for i, l := range strings.Split(mdRaw(richDoc, w), "\n") {
			if open := mdTrack(l, nil); len(open) > 0 {
				t.Errorf("w=%d 第 %d 行结尾仍有样式 %q: %q", w, i, open, l)
			}
		}
	}
}

func TestMarkdownEveryStreamingPrefixRenders(t *testing.T) {
	// 流式输出会把回答一个字一个字地喂进来：任何中间状态都不能崩、不能超宽。
	for _, w := range []int{24, 80} {
		for end := range len(richDoc) + 1 {
			if end < len(richDoc) && !utf8.RuneStart(richDoc[end]) {
				continue
			}
			for i, l := range strings.Split(md(richDoc[:end], w), "\n") {
				if got := ansi.StringWidth(l); got > w {
					t.Fatalf("w=%d 前缀 %d 字节，第 %d 行宽 %d: %q", w, end, i, got, l)
				}
			}
		}
	}
}

func TestMarkdownDeepNesting(t *testing.T) {
	quote := strings.Repeat("> ", 200) + "深层引用"
	list := ""
	for i := range 60 {
		list += strings.Repeat("  ", i) + "- 层\n"
	}
	for _, src := range []string{quote, list} {
		for i, l := range strings.Split(md(src, 30), "\n") {
			if got := ansi.StringWidth(l); got > 30 {
				t.Fatalf("第 %d 行宽 %d 超宽: %q", i, got, l)
			}
		}
	}
}

func TestMarkdownFallsBackToPlainTextOnRendererFailure(t *testing.T) {
	// renderMarkdown 对外永远返回可显示的文本；内部错误只经由 renderMarkdownE 暴露。
	if got := renderMarkdown("正常 **文本**", 40); ansi.Strip(got) != "正常 文本" {
		t.Errorf("got %q", got)
	}
}

func BenchmarkRenderMarkdown(b *testing.B) {
	doc := strings.Repeat(richDoc, 5)
	b.ReportAllocs()
	for range b.N {
		renderMarkdown(doc, 100)
	}
}
