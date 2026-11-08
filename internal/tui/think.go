package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// 思维链流式输出时预览的行数。
const thinkPreviewLines = 3

// drawThink 渲染思维链条目，全程用淡色，和正式回答区分开：
//   - 流式中：只预览最后几行，让用户看到模型在想什么，又不会把对话刷屏；
//   - 结束后：折叠成一行摘要，/think 切换为展开全文。
func (e *entry) drawThink(w int, expand bool) string {
	text := strings.TrimSpace(sanitize(e.text))
	chars := utf8.RuneCountInString(text)

	if e.live {
		lines := strings.Split(wrap(text, w-2), "\n")
		if len(lines) > thinkPreviewLines {
			lines = lines[len(lines)-thinkPreviewLines:]
		}
		head := fmt.Sprintf("思考中… %d 字", chars)
		return styFaint.Render(head + "\n" + hang("", 2, strings.Join(lines, "\n")))
	}
	if expand {
		return styFaint.Render(fmt.Sprintf("思考过程（%d 字）\n", chars) + hang("", 2, wrap(text, w-2)))
	}
	return styFaint.Render(fmt.Sprintf("思考过程 · %d 字（/think 展开）", chars))
}
