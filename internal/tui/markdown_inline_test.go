package tui

import (
	"slices"
	"strings"
	"testing"
)

// sgrBefore 返回 s 里 target 之前最近一个 SGR 序列的参数列表，用来断言“这段文字带了什么样式”。
func sgrBefore(t *testing.T, s, target string) []string {
	t.Helper()
	i := strings.Index(s, target)
	if i < 0 {
		t.Fatalf("找不到 %q: %q", target, s)
	}
	j := strings.LastIndex(s[:i], "\x1b[")
	if j < 0 {
		t.Fatalf("%q 前面没有样式: %q", target, s)
	}
	k := strings.IndexByte(s[j:], 'm')
	return strings.Split(s[j+2:j+k], ";")
}

func TestMarkdownEmphasisStyles(t *testing.T) {
	raw := mdRaw("*斜* **粗** ~~删~~", 60)
	if got := md("*斜* **粗** ~~删~~", 60); got != "斜 粗 删" {
		t.Fatalf("got %q", got)
	}
	for target, param := range map[string]string{"斜": "3", "粗": "1", "删": "9"} {
		if !slices.Contains(sgrBefore(t, raw, target), param) {
			t.Errorf("%q 缺少 SGR %s: %q", target, param, raw)
		}
	}
}

func TestMarkdownStylesCompose(t *testing.T) {
	// 粗体里的行内代码既是粗体也是绿色，不会因为嵌套而丢掉外层样式。
	params := sgrBefore(t, mdRaw("**粗体里的 `code`**", 60), "code")
	if !slices.Contains(params, "1") || !slices.Contains(params, "32") {
		t.Errorf("参数 = %v，应同时含粗体 1 和绿色 32", params)
	}
}

func TestMarkdownHeadingLevelsDiffer(t *testing.T) {
	h1, h3 := sgrBefore(t, mdRaw("# 一", 40), "一"), sgrBefore(t, mdRaw("### 三", 40), "三")
	if !slices.Contains(h1, "4") || slices.Contains(h3, "4") {
		t.Errorf("一级标题应带下划线、三级不带: %v %v", h1, h3)
	}
	if got := md("标题\n===", 40); got != "标题" {
		t.Errorf("setext 标题: %q", got)
	}
}

func TestMarkdownLinks(t *testing.T) {
	for src, want := range map[string]string{
		"[文档](https://a.b/c)":        "文档 (https://a.b/c)",
		"[https://a.b](https://a.b)": "https://a.b",
		"<https://a.b/c>":            "https://a.b/c",
		"<a@b.co>":                   "a@b.co",
		"见 https://go.dev/doc。":      "见 https://go.dev/doc。",
	} {
		if got := md(src, 80); got != want {
			t.Errorf("md(%q) = %q, want %q", src, got, want)
		}
	}
	// 句末的中文句号不属于链接，不能被划进下划线样式里。
	if raw := mdRaw("见 https://go.dev/doc。", 80); !strings.HasSuffix(raw, "\x1b[m。") {
		t.Errorf("句号应在样式之外: %q", raw)
	}
}

func TestMarkdownImage(t *testing.T) {
	if got := md("![图](http://x/y.png)", 80); got != "[图片: 图] (http://x/y.png)" {
		t.Errorf("got %q", got)
	}
	if got := md("![](http://x/y.png)", 80); got != "[图片] (http://x/y.png)" {
		t.Errorf("got %q", got)
	}
}

func TestMarkdownNestedLists(t *testing.T) {
	if got := md("- a\n  - b\n    - c\n      - d", 60); got != "• a\n  ◦ b\n    ▪ c\n      • d" {
		t.Errorf("嵌套圆点按层级轮换: %q", got)
	}
	if got := md("9. a\n10. b\n11. c", 60); got != " 9. a\n10. b\n11. c" {
		t.Errorf("序号右对齐: %q", got)
	}
	if got := md("- [x] 完成\n- [ ] 待办", 60); got != "• [x] 完成\n• [ ] 待办" {
		t.Errorf("任务列表: %q", got)
	}
}

func TestMarkdownLooseAndTightLists(t *testing.T) {
	if got := md("- a\n- b", 60); got != "• a\n• b" {
		t.Errorf("tight: %q", got)
	}
	if got := md("- a\n\n- b", 60); got != "• a\n\n• b" {
		t.Errorf("loose: %q", got)
	}
}

func TestMarkdownNestedQuote(t *testing.T) {
	if got := md("> a\n>\n> > b", 60); got != "│ a\n│\n│ │ b" {
		t.Errorf("got %q", got)
	}
}

func TestMarkdownCodeInsideList(t *testing.T) {
	if got := md("- 步骤\n  ```sh\n  ls\n  ```", 60); got != "• 步骤\n  sh\n    ls" {
		t.Errorf("got %q", got)
	}
	if got := md("    缩进代码", 60); got != "  缩进代码" {
		t.Errorf("缩进代码块: %q", got)
	}
}
