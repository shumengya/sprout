package tui

import (
	"html"
	"regexp"
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark/ast"
	east "github.com/yuin/goldmark/extension/ast"
)

// mdInliner 把行内节点渲染成带样式的字符串。样式沿语法树向下合成（粗体里的行内代码仍是粗体），
// 每个文本叶子只渲染一次，不会出现嵌套样式互相复位的问题。
type mdInliner struct {
	src      []byte
	trimNext bool // 刚输出了任务列表的复选框，紧随其后的文本要去掉开头的空白
}

var mdBreakTag = regexp.MustCompile(`(?i)^<br\s*/?>$`)

func (r *mdInliner) render(n ast.Node, st lipgloss.Style) string {
	var b strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		b.WriteString(r.node(c, st))
	}
	return b.String()
}

func (r *mdInliner) node(n ast.Node, st lipgloss.Style) string {
	switch n := n.(type) {
	case *ast.Text:
		s := mdUnescape(string(n.Value(r.src)))
		brk := n.HardLineBreak() || n.SoftLineBreak()
		if brk {
			s = strings.TrimRight(s, " ")
		}
		if r.trimNext {
			s = strings.TrimLeft(s, " ")
			if s != "" {
				r.trimNext = false
			}
		}
		out := ""
		if s != "" {
			out = st.Render(s)
		}
		if brk { // 软换行也保留：聊天回答里的单个换行几乎总是作者想要的换行
			out += "\n"
		}
		return out

	case *ast.String:
		s := string(n.Value)
		if !n.IsCode() && !n.IsRaw() {
			s = mdUnescape(s)
		}
		return st.Render(s)

	case *ast.CodeSpan:
		var sb strings.Builder
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			if t, ok := c.(*ast.Text); ok {
				sb.Write(t.Value(r.src))
			}
		}
		return mdCodeSpan.Inherit(st).Render(sanitize(strings.ReplaceAll(sb.String(), "\n", " ")))

	case *ast.Emphasis:
		if n.Level >= 2 {
			return r.render(n, st.Bold(true))
		}
		return r.render(n, st.Italic(true))

	case *east.Strikethrough:
		return r.render(n, st.Strikethrough(true).Faint(true))

	case *east.TaskCheckBox:
		r.trimNext = true
		if n.IsChecked {
			return mdCheckOn.Render("[x]") + " "
		}
		return mdFaint.Render("[ ]") + " "

	case *ast.Link:
		label := r.render(n, mdLink.Inherit(st))
		return label + mdURLSuffix(ansi.Strip(label), mdUnescape(string(n.Destination)))

	case *ast.AutoLink:
		return mdLink.Inherit(st).Render(sanitize(string(n.Label(r.src))))

	case *ast.Image:
		alt := strings.TrimSpace(ansi.Strip(r.render(n, lipgloss.NewStyle())))
		label := "[图片]"
		if alt != "" {
			label = "[图片: " + alt + "]"
		}
		dest := mdUnescape(string(n.Destination))
		if dest != "" {
			label += " (" + dest + ")"
		}
		return mdFaint.Render(label)

	case *ast.RawHTML:
		var sb strings.Builder
		for i := 0; i < n.Segments.Len(); i++ {
			seg := n.Segments.At(i)
			sb.Write(seg.Value(r.src))
		}
		raw := sb.String()
		if mdBreakTag.MatchString(raw) {
			return "\n"
		}
		return st.Render(sanitize(raw))
	}
	return r.render(n, st)
}

// mdURLSuffix 在链接文字后面附上目标地址；文字本身就是地址（或邮件地址）时不重复显示。
func mdURLSuffix(label, dest string) string {
	if dest == "" || label == dest || "mailto:"+label == dest {
		return ""
	}
	return mdFaint.Render(" (" + dest + ")")
}

var mdEntity = regexp.MustCompile(`^&(?:#[0-9]{1,7}|#[xX][0-9a-fA-F]{1,6}|[A-Za-z][A-Za-z0-9]{1,31});`)

const mdPunct = "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"

// mdUnescape 处理反斜杠转义（\*）和字符实体（&amp;）。
// 实体可以解出控制字符（&#27;），所以结果要再过一遍 sanitize，防止注入终端转义序列。
func mdUnescape(s string) string {
	if !strings.ContainsAny(s, `\&`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && strings.IndexByte(mdPunct, s[i+1]) >= 0:
			b.WriteByte(s[i+1])
			i += 2
			continue
		case c == '&':
			if ent := mdEntity.FindString(s[i:]); ent != "" {
				// 标准库会把 &notanentity; 当成 &not 加后面的字面文字；真实实体解出来最多两个字符。
				if dec := html.UnescapeString(ent); dec != ent && utf8.RuneCountInString(dec) <= 2 {
					b.WriteString(dec)
					i += len(ent)
					continue
				}
			}
		}
		b.WriteByte(c)
		i++
	}
	return sanitize(b.String())
}
