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
	case arg == "clear" || arg == "clear ":
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

// imageListMarks 把一串待发送图片转成用户条目上显示的说明。
// 显式 /image 附加的和从消息路径自动识别的都要显示，用户才知道模型真的收到了图。
func imageListMarks(imgs []agent.Image) []string {
	if len(imgs) == 0 {
		return nil
	}
	out := make([]string, len(imgs))
	for i, img := range imgs {
		name := img.Name
		if name == "" {
			name = fmt.Sprintf("图片 %d", i+1)
		}
		out[i] = fmt.Sprintf("%s  %s", name, media.Size(len(img.Data)*3/4))
	}
	return out
}

// imageMarks 把事件里的图片说明转成用户条目上显示的文字（重放和实时共用）。
func imageMarks(infos []event.ImageInfo) []string {
	if len(infos) == 0 {
		return nil
	}
	out := make([]string, len(infos))
	for i, in := range infos {
		name := in.Name
		if name == "" {
			name = fmt.Sprintf("图片 %d", i+1)
		}
		out[i] = fmt.Sprintf("%s  %s", name, media.Size(in.Bytes))
	}
	return out
}
