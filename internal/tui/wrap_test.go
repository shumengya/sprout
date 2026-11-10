package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func widths(s string) []int {
	var out []int
	for _, l := range strings.Split(s, "\n") {
		out = append(out, ansi.StringWidth(l))
	}
	return out
}

// 用户截图里的真实文本。旧的 ansi.Wrap 把“日公测，免费游玩，支持”当成一个整体，
// 上一行在第 72 列就换行，后面空着 20 多列。
const genshin = "由米哈游（miHoYo / HoYoverse）开发的开放世界动作 RPG，2020 年 9 月 28日公测，免费游玩，支持 PC、iOS、Android、PS4/PS5（后来也上了 Xbox），账号跨平台互通，最多 4 人联机。"

func TestChineseFillsEachLineInsteadOfBreakingEarly(t *testing.T) {
	// 66~90 列之间旧算法折出来始终是 [66 62 39]：把“日公测，免费游玩，支持”当一个整体挪到下一行，
	// 第一行固定空出一大截。用户的窗口（约 90 列）正好落在这个区间。
	for _, w := range []int{70, 80, 90} {
		old := widths(ansi.Wrap(genshin, w, ""))
		if old[0] > w-4 {
			t.Fatalf("w=%d: 测试前提不成立，旧算法首行宽 %d 没有明显浪费", w, old[0])
		}
		out := wrap(genshin, w)
		ws := widths(out)
		if len(ws) > len(old) {
			t.Errorf("w=%d: 新算法行数 %d 不应多于旧算法 %d", w, len(ws), len(old))
		}
		for i, got := range ws[:len(ws)-1] {
			// 非末行只允许因为英文单词整体或避头尾而少几列，不能空出一大截。
			if got > w || got < w-6 {
				t.Errorf("w=%d 第 %d 行宽 %d，应接近 %d（不超宽、也不提前换行）:\n%s", w, i+1, got, w, out)
			}
		}
	}
}

func TestEnglishWordsAreNeverSplit(t *testing.T) {
	out := wrap("the quick brown fox jumps over the lazy dog", 15)
	for _, l := range strings.Split(out, "\n") {
		for _, word := range strings.Fields(l) {
			if !strings.Contains("the quick brown fox jumps over the lazy dog", word) {
				t.Errorf("单词被拆开: %q in %q", word, l)
			}
		}
	}
	if out != "the quick brown\nfox jumps over\nthe lazy dog" {
		t.Errorf("got %q", out)
	}
}

func TestMixedTextKeepsLatinWordsWholeButBreaksChineseAnywhere(t *testing.T) {
	out := wrap("使用 PowerShell 查看目录里的文件和文件夹的详细信息", 20)
	if !strings.Contains(out, "PowerShell") {
		t.Fatalf("英文单词必须完整: %q", out)
	}
	for i, got := range widths(out) {
		if got > 20 {
			t.Errorf("第 %d 行超宽 %d: %q", i+1, got, out)
		}
	}
}

func TestLongWordsAreHardSplit(t *testing.T) {
	url := "https://example.com/" + strings.Repeat("a", 50)
	out := wrap(url, 20)
	for i, got := range widths(out) {
		if got > 20 {
			t.Errorf("第 %d 行超宽 %d", i+1, got)
		}
	}
	if strings.ReplaceAll(out, "\n", "") != url {
		t.Errorf("硬拆不应丢字符或加字符: %q", out)
	}
}

func TestKinsokuNoClosingPunctuationAtLineStart(t *testing.T) {
	// 宽度恰好让“。”落在下一行行首：必须把前一个字带下去。
	for w := 6; w <= 14; w++ {
		out := wrap("这是一个很长的句子，需要在合适的位置换行。然后继续说话（括号里的内容）结束。", w)
		for _, l := range strings.Split(out, "\n") {
			if l == "" {
				continue
			}
			if r := []rune(l)[0]; strings.ContainsRune(noStart, r) {
				t.Errorf("w=%d 行首出现了不该出现的 %q:\n%s", w, r, out)
			}
			if r := []rune(l); strings.ContainsRune(noEnd, r[len(r)-1]) {
				t.Errorf("w=%d 行尾出现了不该出现的 %q:\n%s", w, r[len(r)-1], out)
			}
		}
	}
}

func TestWrapNeverExceedsWidthAndNeverLosesText(t *testing.T) {
	samples := []string{
		genshin,
		"短",
		"a b c d e f g h i j k l m n o p",
		strings.Repeat("中文", 100),
		strings.Repeat("（）", 30),
		"，，，，，，，，，，，，，，，，，，，，",
		"emoji 😀😀😀 混排 😀 的一行",
		"带    多个    连续空格的    文本",
	}
	for _, s := range samples {
		for _, w := range []int{2, 3, 5, 8, 13, 21, 40, 94} {
			out := wrap(s, w)
			for i, got := range widths(out) {
				if got > w && w >= 2 {
					t.Errorf("w=%d 第 %d 行宽 %d 超宽 (%q):\n%s", w, i+1, got, s, out)
				}
			}
			// 去掉空白后文字一个都不能少。
			if strip(out) != strip(s) {
				t.Errorf("w=%d 丢字或多字:\n原: %q\n后: %q", w, strip(s), strip(out))
			}
		}
	}
}

func strip(s string) string {
	return strings.Join(strings.Fields(s), "")
}

func TestExistingNewlinesAndBlankLinesArePreserved(t *testing.T) {
	out := wrap("第一段\n\n第二段很长很长很长很长很长很长", 10)
	lines := strings.Split(out, "\n")
	if lines[0] != "第一段" || lines[1] != "" || len(lines) < 4 {
		t.Errorf("got %q", lines)
	}
}

func TestLeadingIndentOfSourceLineIsKept(t *testing.T) {
	out := wrap("    缩进的代码行也要折行但保留首行缩进", 14)
	if !strings.HasPrefix(out, "    缩进") {
		t.Errorf("源码行开头的缩进应保留: %q", out)
	}
	for _, l := range strings.Split(out, "\n")[1:] {
		if strings.HasPrefix(l, " ") {
			t.Errorf("折出来的续行开头不该有空格: %q", l)
		}
	}
}

func TestSpacesAtBreakAreDropped(t *testing.T) {
	out := wrap("hello world foo bar", 11)
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, " ") || strings.HasSuffix(l, " ") {
			t.Errorf("行首行尾不应有空格: %q", l)
		}
	}
}

func TestAnsiStylesDoNotCountAndAreNeverSplit(t *testing.T) {
	styled := "\x1b[1m" + strings.Repeat("加粗的中文", 6) + "\x1b[0m 后面是普通文字也很长很长很长"
	out := wrap(styled, 16)
	for i, got := range widths(out) {
		if got > 16 {
			t.Errorf("第 %d 行可见宽度 %d 超宽（转义序列不该计宽度）", i+1, got)
		}
	}
	if ansi.Strip(out) == "" || strings.Count(out, "\x1b[1m") != 1 || strings.Count(out, "\x1b[0m") != 1 {
		t.Errorf("样式序列既不能丢也不能重复: %q", out)
	}
	// 每个转义序列都是完整的：ESC 后面必须紧跟 [，且以字母结束。
	for i := 0; i < len(out); i++ {
		if out[i] == 0x1b && (i+1 >= len(out) || out[i+1] != '[') {
			t.Fatalf("转义序列被截断: %q", out)
		}
	}
}

func TestWrapDegenerateWidths(t *testing.T) {
	// 宽度比一个宽字符还小也不能死循环或 panic，至少保证每个字都出现。
	for _, w := range []int{0, 1} {
		out := wrap("中文abc", w)
		if strip(out) != "中文abc" {
			t.Errorf("w=%d: %q", w, out)
		}
	}
	if wrap("", 10) != "" {
		t.Error("空串应返回空串")
	}
}
