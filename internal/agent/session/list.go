package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Brief 是会话列表里的一行摘要。
type Brief struct {
	ID       string
	Path     string
	Prompt   string // 第一条用户提问，压成一行
	Name     string // 用户起的名字（meta.json 里），没起过为空
	Cwd      string // 会话所属的工作目录
	Provider string
	Model    string
	Updated  time.Time // 文件最后修改时间；恢复后继续追加，所以它就是“最近使用”
	Turns    int
}

// Title 是列表里显示的标题：起过名字用名字，否则用第一条提问。
func (b Brief) Title() string {
	if b.Name != "" {
		return b.Name
	}
	return b.Prompt
}

// promptRunes 是摘要里保留的提问长度。列表按屏幕宽度再截断，这里只防止超长提问拖慢搜索。
const promptRunes = 200

var (
	userPrefix    = []byte(`{"type":"user"`)
	sessionPrefix = []byte(`{"type":"session"`)
)

// List 列出 cwd 对应的会话目录里的全部会话，最近使用的在前。没有任何会话时返回空列表和 nil。
func List(sessionsDir, cwd string) ([]Brief, error) {
	out, err := listGroup(filepath.Join(sessionsDir, Slug(cwd)), cwd)
	if err != nil {
		return nil, err
	}
	sortRecent(out)
	return out, nil
}

// ListAll 列出所有工作目录的会话，最近使用的在前。
func ListAll(sessionsDir string) ([]Brief, error) {
	dirs, err := os.ReadDir(sessionsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Brief
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		bs, err := listGroup(filepath.Join(sessionsDir, d.Name()), "")
		if err != nil {
			return nil, err
		}
		out = append(out, bs...)
	}
	sortRecent(out)
	return out, nil
}

func sortRecent(bs []Brief) {
	slices.SortStableFunc(bs, func(a, b Brief) int { return b.Updated.Compare(a.Updated) })
}

// listGroup 读一个分组目录。cwd 为空时从 meta.json 或会话头记录里取。
func listGroup(dir, cwd string) ([]Brief, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	gm := readMeta(dir)
	if cwd == "" {
		cwd = gm.Cwd
	}
	var out []Brief
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil {
			continue
		}
		b, ok := briefOf(f)
		if !ok {
			continue
		}
		b.Updated = st.ModTime()
		b.Name = gm.Sessions[filepath.Base(f)].Name
		if b.Cwd == "" {
			b.Cwd = cwd
		}
		out = append(out, b)
	}
	return out, nil
}

// briefOf 只用前缀匹配扫一遍文件，不做完整 JSON 解析。记录的第一个字段总是 type，所以可行。
func briefOf(path string) (Brief, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Brief{}, false
	}
	defer f.Close()
	b := Brief{Path: path}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		switch {
		case bytes.HasPrefix(line, sessionPrefix):
			var r rawRec
			if json.Unmarshal(line, &r) == nil {
				b.ID, b.Provider, b.Model, b.Cwd = r.ID, r.Provider, r.Model, r.Cwd
			}
		case bytes.HasPrefix(line, userPrefix):
			b.Turns++
			if b.Prompt == "" {
				var r rawRec
				if json.Unmarshal(line, &r) == nil {
					b.Prompt = oneLine(r.Content, promptRunes)
					if len(r.Images) > 0 {
						if b.Prompt == "" {
							b.Prompt = "[图片]"
						} else {
							b.Prompt += " [图片]"
						}
					}
				}
			}
		}
	}
	return b, b.Turns > 0 // 没有任何提问的会话没有恢复的意义
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > n {
		return string([]rune(s)[:n]) + "…"
	}
	return s
}

// Find 按选择器找一个会话：空串或 "latest" 是最近使用的；纯数字且不超过 3 位是列表序号（从 1 起）；
// 以 .jsonl 结尾的是会话文件路径（选择器跨目录恢复时用，必须在 sessionsDir 下）；其余按会话 ID 前缀匹配。
func Find(sessionsDir, cwd, selector string) (Brief, error) {
	sel := strings.TrimSpace(selector)
	if strings.HasSuffix(sel, ".jsonl") {
		return findPath(sessionsDir, sel)
	}
	list, err := List(sessionsDir, cwd)
	if err != nil {
		return Brief{}, err
	}
	if len(list) == 0 {
		return Brief{}, errors.New("当前目录下还没有可恢复的会话")
	}
	if sel == "" || sel == "latest" {
		return list[0], nil
	}
	if n, err := strconv.Atoi(sel); err == nil && len(sel) <= 3 {
		if n < 1 || n > len(list) {
			return Brief{}, fmt.Errorf("没有序号为 %d 的会话（共 %d 个）", n, len(list))
		}
		return list[n-1], nil
	}
	var hits []Brief
	for _, b := range list {
		if strings.HasPrefix(b.ID, sel) {
			hits = append(hits, b)
		}
	}
	switch len(hits) {
	case 0:
		return Brief{}, fmt.Errorf("没有找到 ID 以 %q 开头的会话", sel)
	case 1:
		return hits[0], nil
	}
	return Brief{}, fmt.Errorf("%q 匹配到 %d 个会话，请多写几位 ID", sel, len(hits))
}

func findPath(sessionsDir, path string) (Brief, error) {
	p, err := inSessions(sessionsDir, path)
	if err != nil {
		return Brief{}, err
	}
	b, ok := briefOf(p)
	if !ok {
		return Brief{}, fmt.Errorf("会话文件 %s 不存在或没有内容", p)
	}
	return b, nil
}

// inSessions 把 path 规整成绝对路径，并确认它是 sessionsDir 下某个分组里的 .jsonl 文件。
// 重命名、删除和按路径恢复都经过它，防止选择器之外的调用方碰到会话目录以外的文件。
func inSessions(sessionsDir, path string) (string, error) {
	root, err := filepath.Abs(sessionsDir)
	if err != nil {
		return "", err
	}
	p, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, p)
	if err != nil || filepath.Ext(p) != ".jsonl" || len(strings.Split(rel, string(filepath.Separator))) != 2 ||
		strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("%s 不是会话目录 %s 下的会话文件", path, root)
	}
	return p, nil
}

// Rename 给会话起名（写进同目录的 meta.json），空名字表示清除。名字压成一行。
func Rename(sessionsDir, path, name string) error {
	p, err := inSessions(sessionsDir, path)
	if err != nil {
		return err
	}
	name = oneLine(name, 80)
	return editMeta(filepath.Dir(p), func(gm *groupMeta) {
		file := filepath.Base(p)
		sm := gm.Sessions[file]
		sm.Name = name
		gm.Sessions[file] = sm
	})
}

// Delete 删除一个会话：JSONL、同名的 .images 与 .agents（子代理记录）目录和 meta.json 里的登记。
// active 是当前正在写的会话文件，删它会让之后的记录写进已删除的文件，所以拒绝。
func Delete(sessionsDir, path, active string) error {
	p, err := inSessions(sessionsDir, path)
	if err != nil {
		return err
	}
	if active != "" {
		if a, err := filepath.Abs(active); err == nil && a == p {
			return errors.New("这是当前正在使用的会话，不能删除；先 /new 开一个新会话")
		}
	}
	if err := os.Remove(p); err != nil {
		return fmt.Errorf("删除会话文件失败: %w", err)
	}
	// 这两个目录是会话自己的附属品，路径由会话文件名唯一确定，不会碰到别的会话。
	base := strings.TrimSuffix(p, ".jsonl")
	if err := os.RemoveAll(base + ".images"); err != nil {
		return fmt.Errorf("会话已删除，但图片目录没删掉: %w", err)
	}
	if err := os.RemoveAll(base + ".agents"); err != nil {
		return fmt.Errorf("会话已删除，但子代理记录目录没删掉: %w", err)
	}
	return editMeta(filepath.Dir(p), func(gm *groupMeta) { delete(gm.Sessions, filepath.Base(p)) })
}
