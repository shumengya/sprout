package tui

import (
	"fmt"
	"strings"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/event"
	"github.com/shumengya/sprout/internal/media"
)

// imageCmd 处理 /image：无参数时列出待发送的图片，带路径时附加一张，clear 清空。
// 读文件是本地 IO，但都是小文件，同步做不会明显卡住界面。
func (m *Model) imageCmd(arg string) {
	switch {
	case arg == "":
		m.add(&entry{kind: kindNotice, text: m.pendingReport()})
		return
	case arg == "clear":
		n := len(m.pending)
		m.pending = nil
		m.add(&entry{kind: kindNotice, text: fmt.Sprintf("已清空 %d 张待发送的图片。", n)})
		return
	}
	path := strings.TrimSpace(strings.Trim(arg, `"'`))
	img, name, err := media.LoadImage(path)
	if err != nil {
		m.add(&entry{kind: kindError, text: err.Error()})
		return
	}
	m.pending = append(m.pending, img)
	note := fmt.Sprintf("已附加图片 %s（%s），共 %d 张。发送时会随消息一起提交。", name, media.Size(len(img.Data)*3/4), len(m.pending))
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
