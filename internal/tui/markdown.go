package tui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// Markdown 渲染：goldmark 按 CommonMark + GFM（表格、删除线、任务列表、裸链接）解析成语法树，
// 这里再把语法树画成终端文本。流式输出时每来一段增量都会整段重渲，
// goldmark 对没写完的语法有明确规则（未闭合的围栏延伸到文末、未闭合的 ** 和反引号原样保留），所以不会吞字符。
//
// 外观参考 opencode：标题加粗着色，列表圆点、引用条、行内代码用不同颜色，代码块带语法高亮。
// 代码块只缩进两格、不画边框，鼠标选中复制时不会带进多余字符。

var mdParser = goldmark.New(goldmark.WithExtensions(extension.GFM))

// 嵌套列表的圆点按层级轮换。
var mdBullets = []string{"•", "◦", "▪"}

// renderMarkdown 把 src 渲染成不超过 w 列的终端文本。
// 渲染器自己出错（panic）时退回纯文本，不能让界面崩掉；测试用 renderMarkdownE 直接看到错误。
func renderMarkdown(src string, w int) string {
	out, err := renderMarkdownE(src, w)
	if err != nil {
		return wrap(sanitize(src), max(w, 1))
	}
	return out
}

func renderMarkdownE(src string, w int) (out string, err error) {
	w = max(w, 1)
	src = sanitize(src) // 幂等；调用方通常已经清理过，这里保证本函数单独使用也安全
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("markdown 渲染失败: %v", p)
		}
	}()

	b := []byte(src)
	doc := mdParser.Parser().Parse(text.NewReader(b))
	r := &mdRenderer{src: b, in: &mdInliner{src: b}}
	lines := r.blocks(doc, w, false)
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	mdClamp(lines, w)
	return strings.Join(lines, "\n"), nil
}

type mdRenderer struct {
	src   []byte
	in    *mdInliner
	base  lipgloss.Style // 外层容器带来的基础样式（引用里的斜体）
	depth int            // 当前所在的列表嵌套层数
}

// blocks 渲染容器的所有子块，块之间空一行；tight 为真（紧凑列表项）时不空行。
func (r *mdRenderer) blocks(parent ast.Node, w int, tight bool) []string {
	var out []string
	for c := parent.FirstChild(); c != nil; c = c.NextSibling() {
		lines := r.block(c, w)
		if len(lines) == 0 {
			continue
		}
		if len(out) > 0 && !tight {
			out = append(out, "")
		}
		out = append(out, lines...)
	}
	return out
}

func (r *mdRenderer) block(n ast.Node, w int) []string {
	switch n := n.(type) {
	case *ast.Paragraph, *ast.TextBlock:
		return r.flow(n, w, r.base)
	case *ast.Heading:
		return r.flow(n, w, mdHeadingStyle(n.Level))
	case *ast.Blockquote:
		return r.quote(n, w)
	case *ast.List:
		return r.list(n, w)
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		return r.code(n, w)
	case *ast.ThematicBreak:
		return []string{mdRule(w)}
	case *ast.HTMLBlock:
		return r.html(n, w)
	case *east.Table:
		return r.table(n, w)
	}
	if n.HasChildren() {
		return r.blocks(n, w, false)
	}
	return nil
}

// flow 渲染一个以行内内容为主的块（段落、标题），并折行。
func (r *mdRenderer) flow(n ast.Node, w int, st lipgloss.Style) []string {
	r.in.trimNext = false
	s := strings.TrimRight(r.in.render(n, st), "\n")
	if strings.TrimSpace(ansi.Strip(s)) == "" {
		return nil
	}
	return mdFlow(s, w)
}

func (r *mdRenderer) quote(n *ast.Blockquote, w int) []string {
	saved := r.base
	r.base = r.base.Italic(true)
	lines := r.blocks(n, max(w-2, 1), false)
	r.base = saved
	bar := mdQuoteBar.Render("│") + " "
	return mdPrefix(lines, bar, bar)
}

func (r *mdRenderer) list(n *ast.List, w int) []string {
	count := 0
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		count++
	}
	digits := len(strconv.Itoa(n.Start + count - 1))
	num := n.Start

	r.depth++
	defer func() { r.depth-- }()

	var out []string
	for item := n.FirstChild(); item != nil; item = item.NextSibling() {
		var marker string
		var mw int
		if n.IsOrdered() {
			marker = mdMarker.Render(fmt.Sprintf("%*d%c", digits, num, n.Marker)) + " "
			mw = digits + 2
			num++
		} else {
			marker = mdMarker.Render(mdBullets[(r.depth-1)%len(mdBullets)]) + " "
			mw = 2
		}
		body := r.blocks(item, max(w-mw, 1), n.IsTight)
		if len(body) == 0 {
			body = []string{""} // 空列表项也要显示标记
		}
		if len(out) > 0 && !n.IsTight {
			out = append(out, "")
		}
		out = append(out, mdPrefix(body, marker, strings.Repeat(" ", mw))...)
	}
	return out
}

func (r *mdRenderer) code(n ast.Node, w int) []string {
	var lang string
	if f, ok := n.(*ast.FencedCodeBlock); ok {
		lang = string(f.Language(r.src))
	}
	var sb strings.Builder
	lines := n.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		sb.Write(seg.Value(r.src))
	}
	code := strings.TrimRight(sb.String(), "\n")

	var out []string
	if lang != "" {
		out = append(out, mdFaint.Render(ansi.Truncate(lang, w, "…")))
	}
	if code == "" {
		return out
	}
	var body []string
	for _, l := range mdHighlight(lang, code) {
		// 代码不重排，只在超宽时硬折行，保留缩进。
		body = append(body, mdSeal(strings.Split(ansi.Hardwrap(l, max(w-2, 1), true), "\n"))...)
	}
	return append(out, mdPrefix(body, "  ", "  ")...)
}

func (r *mdRenderer) html(n *ast.HTMLBlock, w int) []string {
	var sb strings.Builder
	lines := n.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		sb.Write(seg.Value(r.src))
	}
	if n.HasClosure() {
		sb.Write(n.ClosureLine.Value(r.src))
	}
	s := strings.TrimRight(sb.String(), "\n")
	if strings.TrimSpace(s) == "" || strings.HasPrefix(strings.TrimSpace(s), "<!--") {
		return nil // HTML 注释在任何渲染结果里都是不可见的
	}
	return mdFlow(mdFaint.Render(s), w)
}
