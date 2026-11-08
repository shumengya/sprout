// Package tui 是 Bubble Tea v2 终端界面。它只通过 event 协议观察内核，不 import 内核的实现。
//
// 约定：Update 里不做 IO，所有副作用（启动 agent、等待事件）都通过 tea.Cmd。
package tui

import (
	"strings"

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
)

// 颜色用 ANSI 基础色，跟随终端主题，深浅色背景下都可读。
var (
	styUser  = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	styTool  = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	styFaint = lipgloss.NewStyle().Faint(true)
	styErr   = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styWarn  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styTitle = lipgloss.NewStyle().Bold(true)
)

// entry 是 transcript 里的一个条目。渲染结果按宽度缓存，流式追加时只重绘当前条目。
type entry struct {
	kind kind
	text string // user / assistant / notice / error 的正文

	// 工具条目
	name    string
	command string
	running bool
	fin     event.ToolFinished

	cache  string
	cacheW int
	dirty  bool
}

func (e *entry) touch() { e.dirty = true }

func (e *entry) render(w int) string {
	if !e.dirty && e.cacheW == w {
		return e.cache
	}
	e.cache, e.cacheW, e.dirty = e.draw(w), w, false
	return e.cache
}

func (e *entry) draw(w int) string {
	switch e.kind {
	case kindUser:
		return hang(styUser.Render("你> "), 4, wrap(sanitize(e.text), w-4))
	case kindAssistant:
		return wrap(sanitize(e.text), w)
	case kindTool:
		return e.drawTool(w)
	case kindError:
		return styErr.Render(hang("! ", 2, wrap(sanitize(e.text), w-2)))
	}
	return styFaint.Render(wrap(sanitize(e.text), w))
}

// wrap 按显示宽度折行，正确处理中文等宽字符；已有的换行保留。
func wrap(s string, w int) string { return ansi.Wrap(s, max(w, 1), "") }

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
