package tui

import (
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// 代码高亮：chroma 负责切词，颜色自己映射到 ANSI 基础色（跟随终端主题，不用 chroma 的 RGB 主题）。
// 流式输出时整段回答每次都重渲，已经写完的代码块内容不变，所以按（语言，代码）缓存结果。

const mdCacheMax = 64

type mdCacheEntry struct {
	lines []string
	tick  uint64
}

var mdCode = struct {
	sync.Mutex
	lexers map[string]chroma.Lexer // 查不到的语言存 nil，避免反复做全量匹配
	blocks map[string]*mdCacheEntry
	tick   uint64
}{lexers: map[string]chroma.Lexer{}, blocks: map[string]*mdCacheEntry{}}

// mdHighlight 返回逐行带样式的代码，每行自成一体。返回值只读。
func mdHighlight(lang, code string) []string {
	// 围栏的信息串可能带额外属性（```go title=x），只取第一个词作为语言。
	if f := strings.Fields(lang); len(f) > 0 {
		lang = strings.ToLower(f[0])
	} else {
		lang = ""
	}
	key := lang + "\x00" + code

	mdCode.Lock()
	defer mdCode.Unlock()
	mdCode.tick++
	if e, ok := mdCode.blocks[key]; ok {
		e.tick = mdCode.tick
		return e.lines
	}
	lines := mdHighlightUncached(mdLexer(lang), code)
	if len(mdCode.blocks) >= mdCacheMax { // 淘汰最久没用到的；流式中间状态产生的临时条目会先被淘汰
		oldest, oldestTick := "", ^uint64(0)
		for k, e := range mdCode.blocks {
			if e.tick < oldestTick {
				oldest, oldestTick = k, e.tick
			}
		}
		delete(mdCode.blocks, oldest)
	}
	mdCode.blocks[key] = &mdCacheEntry{lines: lines, tick: mdCode.tick}
	return lines
}

// mdLexer 调用时必须持有 mdCode 的锁。
func mdLexer(lang string) chroma.Lexer {
	if lang == "" {
		return nil
	}
	if lx, ok := mdCode.lexers[lang]; ok {
		return lx
	}
	lx := lexers.Get(lang)
	if lx != nil {
		if lx.Config().Name == "plaintext" {
			lx = nil
		} else {
			lx = chroma.Coalesce(lx)
		}
	}
	mdCode.lexers[lang] = lx
	return lx
}

func mdHighlightUncached(lx chroma.Lexer, code string) []string {
	if lx == nil {
		return mdPlainCode(code)
	}
	it, err := lx.Tokenise(nil, code)
	if err != nil {
		return mdPlainCode(code)
	}
	lines := []string{""}
	for tok := it(); tok != chroma.EOF; tok = it() {
		st, styled := mdTokenStyle(tok.Type)
		for i, part := range strings.Split(tok.Value, "\n") {
			if i > 0 {
				lines = append(lines, "")
			}
			if part == "" {
				continue
			}
			if styled {
				part = st.Render(part)
			}
			lines[len(lines)-1] += part
		}
	}
	for len(lines) > 1 && lines[len(lines)-1] == "" { // 词法分析器可能补一个结尾换行
		lines = lines[:len(lines)-1]
	}
	return lines
}

func mdPlainCode(code string) []string {
	lines := strings.Split(code, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = mdCodePlain.Render(l)
		}
	}
	return lines
}

var (
	tkComment  = lipgloss.NewStyle().Faint(true)
	tkKeyword  = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	tkType     = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	tkString   = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	tkNumber   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	tkFunc     = lipgloss.NewStyle().Foreground(lipgloss.Color("12")) // 亮蓝：普通蓝在深色背景上太暗
	tkRed      = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	tkBold     = lipgloss.NewStyle().Bold(true)
	tkItalic   = lipgloss.NewStyle().Italic(true)
	tkNoStyle  = lipgloss.NewStyle()
	tkConstant = tkNumber
)

// mdTokenStyle 把 chroma 的词类映射到颜色。没有样式的词（标识符、标点、空白）返回 false，直接输出原文。
func mdTokenStyle(t chroma.TokenType) (lipgloss.Style, bool) {
	switch {
	case t.InCategory(chroma.Comment):
		if t.InSubCategory(chroma.CommentPreproc) {
			return tkKeyword, true
		}
		return tkComment, true

	case t == chroma.KeywordType:
		return tkType, true
	case t == chroma.KeywordConstant:
		return tkConstant, true
	case t.InCategory(chroma.Keyword):
		return tkKeyword, true

	case t == chroma.LiteralStringEscape:
		return tkNumber, true
	case t.InSubCategory(chroma.LiteralString):
		return tkString, true
	case t.InCategory(chroma.Literal): // 数字等
		return tkNumber, true

	case t.InSubCategory(chroma.NameFunction):
		return tkFunc, true
	case t.InSubCategory(chroma.NameBuiltin), t == chroma.NameClass:
		return tkType, true
	case t == chroma.NameException:
		return tkRed, true
	case t == chroma.NameTag, t == chroma.NameNamespace:
		return tkKeyword, true
	case t == chroma.NameAttribute, t == chroma.NameDecorator, t == chroma.NameConstant:
		return tkNumber, true

	case t == chroma.OperatorWord:
		return tkKeyword, true

	case t == chroma.GenericDeleted, t == chroma.GenericError, t == chroma.GenericTraceback:
		return tkRed, true
	case t == chroma.GenericInserted:
		return tkString, true
	case t == chroma.GenericHeading, t == chroma.GenericSubheading, t == chroma.GenericStrong:
		return tkBold, true
	case t == chroma.GenericEmph:
		return tkItalic, true
	}
	return tkNoStyle, false
}
