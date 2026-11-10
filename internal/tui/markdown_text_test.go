package tui

import (
	"strings"
	"testing"
)

func TestMarkdownLineBreaks(t *testing.T) {
	// 软换行、两个空格、反斜杠、<br> 都要变成换行；聊天回答里单个换行几乎总是作者想要的。
	for _, src := range []string{"a\nb", "a  \nb", "a\\\nb", "a<br>b", "a<br/>b", "a<BR />b"} {
		if got := md(src, 60); got != "a\nb" {
			t.Errorf("md(%q) = %q", src, got)
		}
	}
}

func TestMarkdownRawHTMLStaysLiteral(t *testing.T) {
	// 不认识的标签原样显示：`Vec<String>` 这类文字不能被当成标签吞掉。
	for _, src := range []string{"按 <kbd>Ctrl</kbd> 键", "类型是 Vec<String>"} {
		if got := md(src, 60); got != src {
			t.Errorf("md(%q) = %q", src, got)
		}
	}
	if got := md("<!-- 注释 -->\n\n正文", 60); got != "正文" {
		t.Errorf("HTML 注释应隐藏: %q", got)
	}
	if got := md("<div>x</div>", 60); got != "<div>x</div>" {
		t.Errorf("HTML 块: %q", got)
	}
}

func TestMarkdownEscapesAndEntities(t *testing.T) {
	for src, want := range map[string]string{
		`\*不是斜体\*`:               "*不是斜体*",
		"a &amp; b &copy; &#65;": "a & b © A",
		"&notanentity;":          "&notanentity;",
		"&lte;":                  "&lte;",
	} {
		if got := md(src, 60); got != want {
			t.Errorf("md(%q) = %q, want %q", src, got, want)
		}
	}
}

func TestMarkdownEntityCannotInjectEscapeSequence(t *testing.T) {
	// &#27; 会解码成 ESC；不清理的话模型输出可以往终端注入任意控制序列。
	raw := mdRaw("&#27;[31m红 &#7;&#x1b;", 60)
	if strings.ContainsAny(raw, "\x1b\x07") {
		t.Fatalf("不应出现控制字符: %q", raw)
	}
	if got := md("&#27;[31m红", 60); got != "[31m红" {
		t.Errorf("got %q", got)
	}
}

func TestMarkdownControlCharsInSourceAreStripped(t *testing.T) {
	if raw := mdRaw("正文\x1b[31m红\x07", 60); strings.ContainsAny(raw, "\x1b\x07") {
		t.Errorf("源文本里的控制字符应被清理: %q", raw)
	}
}

func TestMarkdownSealRestoresStylesAcrossLines(t *testing.T) {
	// 一个样式区间被折成多行时，每行都要自带样式的开和关，viewport 才能从任意一行开始显示。
	lines := mdFlow(mdBold.Render("粗体"+strings.Repeat("字", 20)), 10)
	if len(lines) < 3 {
		t.Fatalf("应折成多行: %q", lines)
	}
	for i, l := range lines {
		if !strings.HasPrefix(l, "\x1b[1m") || !strings.HasSuffix(l, "\x1b[m") {
			t.Errorf("第 %d 行没有自带样式: %q", i, l)
		}
	}
}

func TestMarkdownTrackSGR(t *testing.T) {
	for _, tc := range []struct {
		in   string
		open int
	}{
		{"\x1b[31mred", 1},
		{"\x1b[31mred\x1b[0m", 0},
		{"\x1b[1m\x1b[m", 0},
		{"\x1b[1;4mx\x1b[31my", 2},
		{"a\x1b]8;;http://x\x07b\x1b]8;;\x07", 0}, // 超链接不是 SGR，不跨行追踪
	} {
		if got := mdTrack(tc.in, nil); len(got) != tc.open {
			t.Errorf("mdTrack(%q) = %q，期望 %d 个未关闭样式", tc.in, got, tc.open)
		}
	}
}
