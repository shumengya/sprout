package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// wrap 按显示宽度折行。和 ansi.Wrap 的区别：后者把一串连续的中文当成一个“单词”，放不下就整段挪到
// 下一行，上一行末尾会空出一大截；这里中文每个字都可以断，英文单词和数字仍整体保持，
// 并遵守中文标点的避头尾规则。已有的换行保留，ANSI 样式序列不占宽度也不会被拆开。
func wrap(s string, w int) string {
	w = max(w, 1)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = wrapLine(l, w)
	}
	return strings.Join(lines, "\n")
}

type tokKind int

const (
	tokWord  tokKind = iota // 连续的窄字符（英文、数字、半角标点），整体不拆
	tokSpace                // 空白
	tokWide                 // 一个宽字符（中日韩文字、全角标点、emoji），自身就是断点
)

// token 是折行的最小单位。text 里含它前面附带的 ANSI 序列，width 只计可见宽度。
type token struct {
	text  string
	width int
	kind  tokKind
	first rune // 第一个可见字符，避头尾判断用
	last  rune // 最后一个可见字符
}

// 不能出现在行首的字符（右括号、句末标点等）。
const noStart = "，。、；：？！）】》」』”’…—·,.;:?!)]}%"

// 不能出现在行尾的字符（左括号、左引号）。
const noEnd = "（【《「『“‘([{"

// tokenize 把一行文本切成 token。ESC 序列并入下一个可见 token 的前缀；行尾剩余的并入最后一个 token。
func tokenize(line string, w int) []token {
	var toks []token
	var pending strings.Builder // 还没找到归属的 ANSI 序列
	var cur *token

	flush := func() {
		if cur != nil {
			toks = append(toks, *cur)
			cur = nil
		}
	}
	for i := 0; i < len(line); {
		if line[i] == 0x1b {
			n := escLen(line[i:])
			pending.WriteString(line[i : i+n])
			i += n
			continue
		}
		cluster, rest, cw, _ := uniseg.FirstGraphemeClusterInString(line[i:], -1)
		i = len(line) - len(rest)
		r := firstRune(cluster)
		kind := tokWord
		switch {
		case r == ' ' || r == '\t':
			kind = tokSpace
		case cw >= 2:
			kind = tokWide
		}
		// 零宽字符（组合符等）并入前一个 token，不单独成 token。
		if cw == 0 && (cur != nil || len(toks) > 0) {
			if cur == nil {
				prev := toks[len(toks)-1] // 复制出来，不要让指针指向被截掉的那一格
				toks = toks[:len(toks)-1]
				cur = &prev
			}
			cur.text += pending.String() + cluster
			pending.Reset()
			continue
		}
		// 窄字符的单词和空白连续累积；单词不能超过 w，超长的拆成多段，每段都是可断点。宽字符每个自成一个 token。
		canJoin := cur != nil && cur.kind == kind && (kind == tokSpace || (kind == tokWord && cur.width+cw <= w))
		if canJoin {
			cur.text += pending.String() + cluster
			cur.width += cw
			cur.last = r
			pending.Reset()
			continue
		}
		flush()
		cur = &token{text: pending.String() + cluster, width: cw, kind: kind, first: r, last: r}
		pending.Reset()
	}
	if cur != nil {
		cur.text += pending.String()
		flush()
	} else if pending.Len() > 0 && len(toks) > 0 {
		toks[len(toks)-1].text += pending.String()
	}
	return toks
}

func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

// escLen 返回 s 开头的 ESC 序列长度（s[0] 必为 ESC）。覆盖 CSI（颜色等）和 OSC（超链接等）。
func escLen(s string) int {
	if len(s) < 2 {
		return len(s)
	}
	switch s[1] {
	case '[': // CSI：参数字节 0x30-0x3f，中间字节 0x20-0x2f，结束字节 0x40-0x7e
		for i := 2; i < len(s); i++ {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1
			}
		}
	case ']': // OSC：以 BEL 或 ESC \ 结束
		for i := 2; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
	}
	return 2
}

// lineBuf 是正在填充的一行。
type lineBuf struct {
	toks  []token
	width int
}

func (b *lineBuf) add(t token) {
	b.toks = append(b.toks, t)
	b.width += t.width
}

func (b *lineBuf) pop() token {
	t := b.toks[len(b.toks)-1]
	b.toks = b.toks[:len(b.toks)-1]
	b.width -= t.width
	return t
}

// String 渲染这一行。行尾的空白去掉，但其中的 ANSI 序列要保留，否则样式的开关会少一半。
func (b *lineBuf) String() string {
	end := len(b.toks)
	for end > 0 && b.toks[end-1].kind == tokSpace {
		end--
	}
	var sb strings.Builder
	for _, t := range b.toks[:end] {
		sb.WriteString(t.text)
	}
	for _, t := range b.toks[end:] {
		sb.WriteString(escOnly(t.text))
	}
	return sb.String()
}

// escOnly 只保留 s 里的 ANSI 序列。
func escOnly(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			n := escLen(s[i:])
			b.WriteString(s[i : i+n])
			i += n
			continue
		}
		i++
	}
	return b.String()
}

// wrapLine 折一行（不含换行符）。
func wrapLine(line string, w int) string {
	if line == "" || ansi.StringWidth(line) <= w {
		return line
	}
	var (
		out        []string
		cur        lineBuf
		skipSpaces bool            // 刚断行：新行开头的空白丢掉（源码行最开头的缩进除外）
		skippedEsc strings.Builder // 被丢掉的空白里的 ANSI 序列，并入下一个 token
	)
	for _, t := range tokenize(line, w) {
		if skipSpaces && t.kind == tokSpace {
			skippedEsc.WriteString(escOnly(t.text))
			continue
		}
		if skippedEsc.Len() > 0 {
			t.text = skippedEsc.String() + t.text
			skippedEsc.Reset()
		}
		skipSpaces = false

		if len(cur.toks) == 0 || cur.width+t.width <= w {
			cur.add(t)
			continue
		}
		if t.kind == tokSpace { // 空白放不下：在这里断开，空白本身丢掉
			out = append(out, cur.String())
			cur, skipSpaces = lineBuf{}, true
			skippedEsc.WriteString(escOnly(t.text))
			continue
		}

		carry := kinsokuCarry(&cur, t, w)
		out = append(out, cur.String())
		cur = lineBuf{}
		for _, c := range carry {
			cur.add(c)
		}
		cur.add(t)
	}
	if len(cur.toks) > 0 || len(out) == 0 {
		out = append(out, cur.String())
	} else if skippedEsc.Len() > 0 {
		out[len(out)-1] += skippedEsc.String()
	}
	return strings.Join(out, "\n")
}

// kinsokuCarry 实现避头尾：t 放不下要换行时，如果换行会让禁止出现在行首的标点（，。）等）落到新行开头，
// 或让禁止出现在行尾的括号（（「等）留在旧行末尾，就把旧行末尾的 token 挪到新行。
// 只在 t 紧贴着前一个 token（中间没有空白）时才需要。最多挪 3 个，挪了会放不下就不挪。
func kinsokuCarry(cur *lineBuf, t token, w int) []token {
	var carry []token
	for len(cur.toks) > 1 && len(carry) < 3 {
		last := cur.toks[len(cur.toks)-1]
		if last.kind == tokSpace {
			break
		}
		head := t // 挪完之后将成为新行第一个 token 的
		if len(carry) > 0 {
			head = carry[0]
		}
		if !strings.ContainsRune(noStart, head.first) && !strings.ContainsRune(noEnd, last.last) {
			break
		}
		carry = append([]token{cur.pop()}, carry...)
	}
	width := t.width
	for _, c := range carry {
		width += c.width
	}
	if width > w { // 挪过去反而放不下：还原，宁可违反避头尾也不能超宽
		for _, c := range carry {
			cur.add(c)
		}
		return nil
	}
	return carry
}
