package tui

import (
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/event"
	"github.com/shumengya/sprout/internal/media"
)

// 读图片可能要把大照片缩放、重新编码（千万像素的照片约 1 秒），所以都放在 Cmd 里做，
// 结果以 imageMsg 交回 Update；读的时候界面照常响应。
type imageSource int

const (
	fromCommand   imageSource = iota // /image 路径
	fromPaste                        // 粘贴的内容里带图片路径
	fromClipboard                    // Ctrl+V / Alt+V 读剪贴板里的图片
)

// imageMsg 是后台读图的结果。insert 非空时插进输入框（粘贴改写后的内容）。
type imageMsg struct {
	from   imageSource
	insert string
	imgs   []agent.Image
	err    error
}

// imageCmd 处理 /image：无参数时列出待发送的图片，带路径时在后台读取并附加，clear 清空。
func (m *Model) imageCmd(arg string) tea.Cmd {
	switch {
	case arg == "":
		m.add(&entry{kind: kindNotice, text: m.pendingReport()})
		return nil
	case arg == "clear":
		n := len(m.pending)
		m.pending = nil
		m.add(&entry{kind: kindNotice, text: fmt.Sprintf("已清空 %d 张待发送的图片。", n)})
		return nil
	}
	path := strings.TrimSpace(strings.Trim(arg, `"'`))
	m.status = "正在读取图片…"
	return func() tea.Msg {
		img, _, err := media.LoadImage(path)
		if err != nil {
			return imageMsg{from: fromCommand, err: err}
		}
		return imageMsg{from: fromCommand, imgs: []agent.Image{img}}
	}
}

// onImages 把后台读到的图片接进待发送列表。
func (m *Model) onImages(msg imageMsg) {
	m.status = ""
	if msg.err != nil {
		if msg.from == fromClipboard { // “剪贴板里没有图片”这类只是提示，放状态行
			m.status = msg.err.Error()
			return
		}
		m.add(&entry{kind: kindError, text: msg.err.Error()})
		return
	}
	m.pending = append(m.pending, msg.imgs...)
	if msg.insert != "" {
		m.in.InsertString(msg.insert)
	}
	if msg.from == fromClipboard {
		m.status, m.statusOK = fmt.Sprintf("已粘贴剪贴板图片（%s）", media.Size(len(msg.imgs[0].Data)*3/4)), true
		if !m.opts.Info.Vision {
			m.status, m.statusOK = m.status+"；当前模型没有标注 vision，发送时会被替换成占位文本", false
		}
		return
	}
	if msg.from != fromCommand {
		return
	}
	img := msg.imgs[0]
	note := fmt.Sprintf("已附加图片 %s（%s），共 %d 张。发送时会随消息一起提交。", img.Name, media.Size(len(img.Data)*3/4), len(m.pending))
	if !m.opts.Info.Vision {
		note += "\n当前模型没有标注 vision，发送时图片会被替换成占位文本（models.jsonc 里给该模型加 \"vision\": true）。"
	}
	m.add(&entry{kind: kindNotice, text: note})
}

// pendingReport 列出待发送的图片。
func (m *Model) pendingReport() string {
	if len(m.pending) == 0 {
		return "没有待发送的图片。用 /image 路径 附加一张（png / jpeg / gif / webp），/image clear 清空。"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "待发送的图片（%d 张）：", len(m.pending))
	for i, img := range m.pending {
		fmt.Fprintf(&b, "\n  %d  %s  %s", i+1, img.Name, media.Size(len(img.Data)*3/4))
	}
	b.WriteString("\n发送下一条消息时会一起提交；/image clear 清空。")
	return b.String()
}

// pasteCmd 在后台读出粘贴内容里的图片路径，改写成 [文件名] 后插进输入框。
// 只拦截粘贴：逐键输入时路径还没打完，中途替换会打断用户。
func (m *Model) pasteCmd(content string) tea.Cmd {
	room := max(media.MaxAutoImages-len(m.pending), 0)
	m.status = "正在读取图片…"
	return func() tea.Msg {
		text, imgs := pasteImages(content, room)
		return imageMsg{from: fromPaste, insert: text, imgs: imgs}
	}
}

// pasteImages 把粘贴内容里现有的图片路径换成 [文件名]，返回改写后的内容和读到的图片，最多 room 张。
// 读不了的图片（损坏、太大）保留原路径，提交时 AutoAttach 会说明原因。
func pasteImages(content string, room int) (string, []agent.Image) {
	var imgs []agent.Image
	for _, p := range media.FindImagePaths(content) {
		if len(imgs) >= room {
			break
		}
		img, _, err := media.LoadImage(p)
		if err != nil {
			continue
		}
		// 带引号粘贴（资源管理器“复制为路径”）时连引号一起换掉，否则输入框里会剩下 "[a.png]"。
		frag := p
		for _, q := range []string{`"`, `'`} {
			if strings.Contains(content, q+p+q) {
				frag = q + p + q
				break
			}
		}
		img.Source = frag
		content = strings.Replace(content, frag, pasteMark(img), 1)
		imgs = append(imgs, img)
	}
	return content, imgs
}

// clipboardCmd 在后台读剪贴板里的图片。图片在输入框里显示成 [剪贴板图片 N]，这个标记原样发给模型，
// 告诉它图片在正文里的位置；删掉标记就不发这张图。剪贴板里是从文件管理器复制的文件时，按粘贴这些路径处理。
// textFallback（按的是 Ctrl+V）时没有图片就退回粘贴文字：终端没把 Ctrl+V 拦下来做粘贴时，文字粘贴也要能用。
func (m *Model) clipboardCmd(textFallback bool) tea.Cmd {
	if len(m.pending) >= media.MaxAutoImages {
		m.status = fmt.Sprintf("一条消息最多附加 %d 张图片", media.MaxAutoImages)
		return nil
	}
	read := m.opts.ClipboardImage
	m.clipSeq++
	name := fmt.Sprintf("剪贴板图片 %d", m.clipSeq) // 序号在本次运行内递增，同一条消息里的标记不会重名
	m.status = "正在读取剪贴板…"
	return func() tea.Msg {
		data, paths, err := read()
		switch {
		case err != nil:
			return imageMsg{from: fromClipboard, err: err}
		case len(data) > 0:
			img, err := media.Prepare(data, name)
			if err != nil {
				return imageMsg{from: fromClipboard, err: err}
			}
			img.Source = pasteMark(img)
			return imageMsg{from: fromClipboard, insert: img.Source, imgs: []agent.Image{img}}
		case len(paths) > 0:
			return tea.PasteMsg{Content: quotePaths(paths)}
		case textFallback:
			if text, err := clipboard.ReadAll(); err == nil && text != "" {
				return tea.PasteMsg{Content: text}
			}
		}
		return imageMsg{from: fromClipboard, err: errors.New("剪贴板里没有图片")}
	}
}

// quotePaths 把复制的文件拼成一段粘贴内容；带空格的路径加引号，FindImagePaths 才能整段认出来。
func quotePaths(paths []string) string {
	out := make([]string, len(paths))
	for i, p := range paths {
		if strings.ContainsAny(p, " \t") {
			p = `"` + p + `"`
		}
		out[i] = p
	}
	return strings.Join(out, " ")
}

// pasteMark 是粘贴图片在输入框里的显示形式。
func pasteMark(img agent.Image) string { return "[" + img.Name + "]" }

// inputView 是输入框的渲染结果：待发送图片的 [文件名] 上绿色，和对话区保持一致。
// textarea 自己只输出纯文本样式，所以在它渲染完之后替换；样式序列不占宽度，
// 光标位置和折行都不受影响。标记被拆到两行时匹配不到，保持原样即可。
func (m *Model) inputView() string {
	out := m.in.View()
	for _, img := range m.pending {
		mark := pasteMark(img)
		out = strings.ReplaceAll(out, mark, styImg.Render(mark))
	}
	return out
}

// expandPasted 把输入框里粘贴图片的 [文件名] 换回原始路径，发给模型和写进会话的仍是用户粘贴的原文。
// 标记被用户删掉的图片不再发送；/image 附加的图片（没有 Source）不在正文里，总是保留。
func expandPasted(text string, imgs []agent.Image) (string, []agent.Image) {
	var keep []agent.Image
	for _, img := range imgs {
		if img.Source == "" {
			keep = append(keep, img)
			continue
		}
		mark := pasteMark(img)
		i := strings.Index(text, mark)
		if i < 0 {
			continue
		}
		text = text[:i] + img.Source + text[i+len(mark):]
		keep = append(keep, img)
	}
	return text, keep
}

// imageListMarks 把待发送的图片转成用户条目上的显示信息。
func imageListMarks(imgs []agent.Image) []entryImage {
	if len(imgs) == 0 {
		return nil
	}
	out := make([]entryImage, len(imgs))
	for i, img := range imgs {
		out[i] = entryImage{name: displayName(img.Name, i), source: img.Source, size: media.Size(len(img.Data) * 3 / 4)}
	}
	return out
}

// imageMarks 把事件里的图片说明转成用户条目上的显示信息（重放和实时共用）。
func imageMarks(infos []event.ImageInfo) []entryImage {
	if len(infos) == 0 {
		return nil
	}
	out := make([]entryImage, len(infos))
	for i, in := range infos {
		out[i] = entryImage{name: displayName(in.Name, i), source: in.Source, size: media.Size(in.Bytes)}
	}
	return out
}

func displayName(name string, i int) string {
	if name == "" {
		return fmt.Sprintf("图片 %d", i+1)
	}
	return name
}

// imageMark 是正文里某个图片路径被换成的标记。
type imageMark struct {
	plain  string // 折行时用的纯文本，如 [截图.png]
	styled string // 着色后的同一段，如绿色 [截图.png]
}

// drawUserText 渲染用户正文：图片的原始路径原位换成绿色的 [文件名]。
//
// 先在纯文本上替换和折行——样式序列会占宽度计算，先着色再折行容易被拆坏；
// 折完再逐行给标记和普通文字分别上色。
func drawUserText(text string, imgs []entryImage, w int) string {
	text = sanitize(text)
	var marks []imageMark
	for _, img := range imgs {
		src := sanitize(img.source)
		if src == "" || !strings.Contains(text, src) {
			continue
		}
		plain := "[" + sanitize(img.name) + "]"
		text = strings.ReplaceAll(text, src, plain)
		marks = append(marks, imageMark{plain: plain, styled: styImg.Render(plain)})
	}

	lines := strings.Split(wrap(text, w), "\n")
	for i, line := range lines {
		lines[i] = colorizeImageMarks(line, marks)
	}
	return strings.Join(lines, "\n")
}

// colorizeImageMarks 给一行里的图片标记上绿色，其余部分套用户色。
// 标记本身很短、折行时不会被拆开，所以逐行查找足够。
func colorizeImageMarks(line string, marks []imageMark) string {
	var b strings.Builder
	for len(line) > 0 {
		idx := -1
		var hit imageMark
		for _, mk := range marks {
			if j := strings.Index(line, mk.plain); j >= 0 && (idx < 0 || j < idx) {
				idx, hit = j, mk
			}
		}
		if idx < 0 {
			b.WriteString(styUser.Render(line))
			break
		}
		if idx > 0 {
			b.WriteString(styUser.Render(line[:idx]))
		}
		b.WriteString(hit.styled)
		line = line[idx+len(hit.plain):]
	}
	return b.String()
}
