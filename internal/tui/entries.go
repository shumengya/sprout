// Package tui 是 Bubble Tea v2 终端界面。它只通过 event 协议观察内核，不 import 内核的实现。
//
// 约定：Update 里不做 IO，所有副作用（启动 agent、等待事件）都通过 tea.Cmd。
package tui

import (
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shumengya/sprout/internal/agent/event"
)

type kind int

const (
	kindUser kind = iota
	kindAssistant
	kindTool
	kindNotice
	kindError
	kindThink // 模型的思维链（reasoning_content）
)

// 颜色用 ANSI 基础色，跟随终端主题，深浅色背景下都可读。
var (
	styUser  = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	styTool  = lipgloss.NewStyle().Foreground(lipgloss.Color("5")).Bold(true)
	styFaint = lipgloss.NewStyle().Faint(true)
	styErr   = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styWarn  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
)

// entry 是 transcript 里的一个条目。渲染结果按宽度缓存，流式追加时只重绘当前条目。
type entry struct {
	kind kind
	text string // user / assistant / notice / error 的正文

	// images 是用户消息附带的图片说明（如 "screenshot.png  1.2 MB"），每张一行。
	images []string

	// 工具条目
	name    string
	command string
	running bool
	fin     event.ToolFinished

	// 思维链条目：live 表示还在流式输出，此时只预览最后几行。
	live bool
	// open 表示展开全文（点击条目或 /think 切换）；thinkStart 是第一段增量到达的时间，
	// thinkDur 是思考耗时（从会话恢复或非流式给出时没有，为 0）；thinkTokens 是服务端报告的思维链 token 数，0 表示未报告；
	// thinkEst 是内核的估算（MessageCompleted.ReasoningTokensEst），服务端没报告时显示它。
	open        bool
	thinkStart  time.Time
	thinkDur    time.Duration
	thinkTokens int
	thinkEst    int

	cache    string
	cacheW   int
	cacheExp bool // 渲染缓存对应的展开状态
	dirty    bool
}

func (e *entry) touch() { e.dirty = true }

// render 返回条目在宽度 w 下的渲染结果，按宽度和展开状态缓存。
func (e *entry) render(w int) string {
	if !e.dirty && e.cacheW == w && e.cacheExp == e.open {
		return e.cache
	}
	e.cache, e.cacheW, e.cacheExp, e.dirty = e.draw(w), w, e.open, false
	return e.cache
}

func (e *entry) draw(w int) string {
	switch e.kind {
	case kindUser:
		// 不加前缀，靠颜色和助手回答区分；助手回答保持终端默认前景色。
		body := styUser.Render(wrap(sanitize(e.text), w))
		if len(e.images) == 0 {
			return body
		}
		var b strings.Builder
		for _, img := range e.images {
			b.WriteString(styFaint.Render(wrap("[图片] "+sanitize(img), w)))
			b.WriteString("\n")
		}
		b.WriteString(body)
		return b.String()
	case kindAssistant:
		return renderMarkdown(sanitize(e.text), w)
	case kindTool:
		return e.drawTool(w)
	case kindThink:
		return e.drawThink(w)
	case kindError:
		return styErr.Render(hang("! ", 2, wrap(sanitize(e.text), w-2)))
	}
	return styFaint.Render(wrap(sanitize(e.text), w))
}

// hang 给多行文本加首行前缀和后续行缩进。
func hang(prefix string, indent int, body string) string {
	lines := strings.Split(body, "\n")
	pad := strings.Repeat(" ", indent)
	for i := range lines {
		if i == 0 {
			lines[i] = prefix + lines[i]
		} else {
			lines[i] = pad + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

// sanitize 去掉会破坏终端布局的内容：ANSI 转义、制表符、回车和其他控制字符。
// 命令输出可能带颜色码，直接渲染会让宽度计算失真。
func sanitize(s string) string {
	s = ansi.Strip(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteRune(r)
		case r == '\t':
			b.WriteString("    ")
		case r < 0x20, r == 0x7f, r >= 0x80 && r < 0xa0:
			// 丢弃控制字符，含 \r
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
