package tui

import (
	"fmt"
	"strings"
	"testing"
)

func TestMarkdownCodeHighlightsKnownLanguage(t *testing.T) {
	raw := mdRaw("```go\nfunc main() {}\n```", 60)
	if !strings.Contains(raw, "\x1b[35mfunc") {
		t.Errorf("关键字应着色: %q", raw)
	}
	if got := md("```go\nfunc main() {}\n```", 60); got != "go\n  func main() {}" {
		t.Errorf("去掉样式后应是原文: %q", got)
	}
}

func TestMarkdownCodeWithoutLanguageUsesPlainColor(t *testing.T) {
	for _, src := range []string{"```\nfunc x\n```", "```nosuchlang\nfunc x\n```", "```text\nfunc x\n```"} {
		raw := mdRaw(src, 60)
		if strings.Contains(raw, "\x1b[35m") {
			t.Errorf("%q 不应有关键字着色: %q", src, raw)
		}
		if !strings.Contains(raw, "\x1b[32mfunc x") {
			t.Errorf("%q 应整体用代码色: %q", src, raw)
		}
	}
}

func TestMarkdownCodeWrapsLongLinesWithoutLosingText(t *testing.T) {
	long := strings.Repeat("x", 100)
	got := md("```\n"+long+"\n```", 30)
	if strings.ReplaceAll(strings.ReplaceAll(got, "\n", ""), " ", "") != long {
		t.Errorf("折行丢字:\n%s", got)
	}
	for _, l := range strings.Split(got, "\n") {
		if !strings.HasPrefix(l, "  ") {
			t.Errorf("续行也要缩进: %q", l)
		}
	}
}

func TestMarkdownCodeKeepsBlankLinesClean(t *testing.T) {
	got := md("```\na\n\nb\n```", 60)
	if got != "  a\n\n  b" {
		t.Errorf("代码里的空行不留缩进空格: %q", got)
	}
}

func TestMarkdownHighlightCache(t *testing.T) {
	a, b := mdHighlight("go", "var x = 1"), mdHighlight("go", "var x = 1")
	if &a[0] != &b[0] {
		t.Error("相同内容应命中缓存")
	}

	hot := mdHighlight("go", "var hot = 1")
	for i := range 200 { // 远超容量；流式中间状态每次都是新内容
		mdHighlight("go", fmt.Sprintf("var x%d = 1", i))
		if i%10 == 0 {
			mdHighlight("go", "var hot = 1") // 已完成的代码块每次重渲都会被访问
		}
	}
	mdCode.Lock()
	size := len(mdCode.blocks)
	mdCode.Unlock()
	if size > mdCacheMax {
		t.Errorf("缓存条目 %d 超过上限 %d", size, mdCacheMax)
	}
	if again := mdHighlight("go", "var hot = 1"); &again[0] != &hot[0] {
		t.Error("常被访问的条目不应被淘汰")
	}
}
