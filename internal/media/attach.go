package media

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/shumengya/sprout/internal/agent"
)

// MaxAutoImages 是一条消息里自动附加图片的上限。防止一段长文本里提到一堆路径时
// 把请求撑爆（每张图最多几 MB），超出的会以 skipped 说明告诉用户。
const MaxAutoImages = 10

// Attached 描述一张已经附加到消息上的图片。
type Attached struct {
	Name  string // 文件名，给用户看
	Bytes int
}

// FindImagePaths 在文本里找出存在的图片文件路径。只返回确实存在的文件：
// 不存在的路径留在文本里交给模型判断（用户可能只是在引用一个还没创建的文件）。
//
// 中文没有空格分词，“看这张图：D:\\截图.png 这张图的内容”会整段粘在一起，
// 所以先按引号和空白粗略切分，再从每个可能的位置当起点、逐字符回退找能对上真实文件的路径。
func FindImagePaths(text string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || !isExistingImage(p) {
			return
		}
		key := filepath.Clean(p)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, p)
	}

	for _, seg := range segments(text) {
		addFromSegment(seg, add)
	}
	return out
}

// segments 按文本顺序切出候选片段：引号内整体算一段（可能含空格的路径），
// 引号外按空白切。顺序保持原样，自动附加的图片顺序和用户写的顺序一致。
func segments(text string) []string {
	var out []string
	var field strings.Builder
	flush := func() {
		if s := field.String(); s != "" {
			out = append(out, s)
		}
		field.Reset()
	}
	for i := 0; i < len(text); {
		if c := text[i]; c == '"' || c == '\'' {
			j := strings.IndexByte(text[i+1:], c)
			flush()
			if j < 0 { // 引号没配对：当普通字符处理，别丢掉后面的内容
				field.WriteByte(c)
				i++
				continue
			}
			if quoted := text[i+1 : i+1+j]; quoted != "" {
				out = append(out, quoted)
			}
			i += j + 2
			continue
		}
		if text[i] == ' ' || text[i] == '\t' || text[i] == '\n' || text[i] == '\r' {
			flush()
			i++
			continue
		}
		field.WriteByte(text[i])
		i++
	}
	flush()
	return out
}

// addFromSegment 从一段文本里找存在的图片路径。先整段试，再为每个可能的起点
// （盘符、每个分隔符之后）截出后缀，从长到短回退，直到命中真实文件。
// 回退时先看扩展名，扩展名不对直接跳过，不做无谓的 stat。
func addFromSegment(seg string, add func(string)) {
	if addTrimmed(seg, add) {
		return
	}
	for _, cand := range pathStarts(seg) {
		if addTrimmed(cand, add) {
			return // 一段里只取最长的那个路径：更短的后缀只是它的尾巴
		}
	}
}

// addTrimmed 从 cand 尾部逐字符回退，找到第一个真实存在的图片文件就 add 并返回 true。
// 回退长度有上限：粘连的文字通常只有几个字，再长就不是“路径后面跟了句话”了。
func addTrimmed(cand string, add func(string)) bool {
	cand = strings.TrimSpace(cand)
	r := []rune(cand)
	for n := len(r); n >= 4 && len(r)-n <= 64; n-- {
		p := strings.TrimRight(string(r[:n]), " ")
		if agent.MediaTypeByExt(p) == "" {
			continue // 扩展名不对，省掉一次 stat
		}
		if isExistingImage(p) {
			add(p)
			return true
		}
	}
	return false
}

// pathStarts 给出一段文本里可能是路径起点的后缀：盘符（C:\、D:/）和每个分隔符之后的位置。
// 中文和路径粘连时，真正的起点一定在其中。
func pathStarts(s string) []string {
	var out []string
	for i := 1; i < len(s); i++ {
		c := s[i]
		if c == ':' && i+1 < len(s) && (s[i+1] == '/' || s[i+1] == '\\') {
			out = append(out, s[i-1:])
			continue
		}
		if c == '/' || c == '\\' {
			out = append(out, s[i-1:])
		}
	}
	return out
}

// extractQuoted 把引号里的内容交给 add，并返回去掉这些片段后的文本。
// 同时认双引号和单引号：终端里从资源管理器复制出来的路径两种都可能出现。
func extractQuoted(text string, add func(string)) string {
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c != '"' && c != '\'' {
			b.WriteByte(c)
			continue
		}
		j := strings.IndexByte(text[i+1:], c)
		if j < 0 {
			b.WriteByte(c)
			continue
		}
		add(text[i+1 : i+1+j])
		b.WriteByte(' ')
		i += j + 1
	}
	return b.String()
}

func isExistingImage(p string) bool {
	if agent.MediaTypeByExt(p) == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// AutoAttach 把文本里提到的图片读进来，追加到 in.Images（原有图片保留在前）。
// 返回附加结果和被跳过的原因：跳过的路径仍留在文本里，用户不会因此丢掉信息。
// 读取失败（超限、损坏）只记原因，不让整条消息失败——用户可能只是随口提到了一个文件名。
func AutoAttach(in agent.Input) (agent.Input, []Attached, []string) {
	paths := FindImagePaths(in.Text)
	if len(paths) == 0 {
		return in, nil, nil
	}
	// 已经显式附加过的图片不重复读。
	seen := map[string]bool{}
	for _, img := range in.Images {
		seen[img.Data] = true
	}
	var attached []Attached
	var skipped []string
	for _, p := range paths {
		if len(in.Images) >= MaxAutoImages {
			skipped = append(skipped, filepath.Base(p)+"：一条消息最多附加 "+strconv.Itoa(MaxAutoImages)+" 张图片")
			continue
		}
		img, name, err := LoadImage(p)
		if err != nil {
			skipped = append(skipped, err.Error())
			continue
		}
		if seen[img.Data] {
			continue
		}
		seen[img.Data] = true
		in.Images = append(in.Images, img)
		attached = append(attached, Attached{Name: name, Bytes: len(img.Data) * 3 / 4})
	}
	return in, attached, skipped
}
