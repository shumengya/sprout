package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// 思维链流式输出时预览的行数。
const thinkPreviewLines = 3

// drawThink 渲染思维链条目，全程用淡色，和正式回答区分开：
//   - 流式中：只预览最后几行，让用户看到模型在想什么，又不会把对话刷屏；
//   - 结束后：一行“已思考 12秒 340token：开头内容…”，点击条目（或 /think）展开全文，再点一次收起。
func (e *entry) drawThink(w int) string {
	text := strings.TrimSpace(sanitize(e.text))

	if e.live {
		lines := strings.Split(wrap(text, w-2), "\n")
		if len(lines) > thinkPreviewLines {
			lines = lines[len(lines)-thinkPreviewLines:]
		}
		return thinkBlock(fmt.Sprintf("思考中… %d 字", utf8.RuneCountInString(text)), lines)
	}

	head := e.thinkHead()
	if e.open {
		var body []string
		if text != "" {
			body = strings.Split(wrap(text, w-2), "\n")
		}
		return thinkBlock(ansi.Truncate(head, w, "…"), body)
	}
	// 折叠时只显示一行：思考内容压成一行接在后面，放不下的用省略号代替。
	line := head
	if flat := strings.Join(strings.Fields(text), " "); flat != "" {
		line += "：" + flat
	}
	return styFaint.Render(ansi.Truncate(line, w, "…"))
}

// thinkBlock 渲染头部加缩进两格的正文，逐行套淡色。不把整块交给 lipgloss：
// 它会把每行用空格补到同一宽度，复制时会带上多余的空格。
func thinkBlock(head string, body []string) string {
	out := make([]string, 0, len(body)+1)
	out = append(out, styFaint.Render(head))
	for _, l := range body {
		if l == "" {
			out = append(out, "")
			continue
		}
		out = append(out, styFaint.Render("  "+l))
	}
	return strings.Join(out, "\n")
}

// thinkHead 是“已思考 12秒 340 token”：耗时未知（恢复的会话、非流式服务）时省略；
// token 数优先用服务端报告的，没报告时用内核的估算并标“约”；两者都没有（思考到一半被取消）时不显示。
func (e *entry) thinkHead() string {
	parts := []string{"已思考"}
	if e.thinkDur > 0 {
		parts = append(parts, fmtThinkDur(e.thinkDur))
	}
	switch {
	case e.thinkTokens > 0:
		parts = append(parts, fmtTokens(e.thinkTokens)+" token")
	case e.thinkEst > 0:
		parts = append(parts, "约"+fmtTokens(e.thinkEst)+" token")
	}
	return strings.Join(parts, " ")
}

// rowSpan 是一个条目在对话内容里占的行范围 [start, end)。
type rowSpan struct{ start, end int }

// entryAt 返回内容第 row 行所在的条目及其下标；点在条目之间的空行或内容之外时返回 -1。
func (m *Model) entryAt(row int) int {
	for i, s := range m.spans {
		if row >= s.start && row < s.end {
			return i
		}
	}
	return -1
}

// toggleThinkAt 展开或收起内容第 row 行所在的思考条目；点中的不是已结束的思考条目时什么也不做。
// 切换后让这个条目的开头停在屏幕上原来的位置，内容不会跳走；展开前开头已经滚出屏幕上方
// （点在长思考的中间收起）时，收起后把它放到对话区顶部。
func (m *Model) toggleThinkAt(row int) {
	i := m.entryAt(row)
	if i < 0 || i >= len(m.entries) {
		return
	}
	e := m.entries[i]
	if e.kind != kindThink || e.live {
		return
	}
	screen := max(m.spans[i].start-m.vp.YOffset(), 0)
	unseen := m.unseen

	e.open = !e.open
	e.touch()
	m.refresh()

	m.vp.SetYOffset(m.spans[i].start - screen)
	m.syncFollow()
	if !m.follow {
		m.unseen = unseen // 行数变化是用户自己展开造成的，不是新内容
	}
}

// fmtThinkDur 把思考耗时写成“12秒”或“2分05秒”，不足一秒按一秒算。
func fmtThinkDur(d time.Duration) string {
	s := max(int(d.Round(time.Second)/time.Second), 1)
	if s < 60 {
		return fmt.Sprintf("%d秒", s)
	}
	return fmt.Sprintf("%d分%02d秒", s/60, s%60)
}
