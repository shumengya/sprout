package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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
	Prompt   string // 第一条用户提问
	Provider string
	Model    string
	Updated  time.Time // 文件最后修改时间；恢复后继续追加，所以它就是“最近使用”
	Turns    int
}

// maxBriefs 限制列表读取的会话数，避免历史很多时 /resume 变慢。
const maxBriefs = 30

var (
	userPrefix    = []byte(`{"type":"user"`)
	sessionPrefix = []byte(`{"type":"session"`)
)

// List 列出 cwd 对应的会话目录里最近使用的会话，新的在前。没有任何会话时返回空列表和 nil。
func List(sessionsDir, cwd string) ([]Brief, error) {
	files, err := filepath.Glob(filepath.Join(sessionsDir, Slug(cwd), "*.jsonl"))
	if err != nil {
		return nil, err
	}
	type fileTime struct {
		path string
		mod  time.Time
	}
	var fts []fileTime
	for _, f := range files {
		if st, err := os.Stat(f); err == nil {
			fts = append(fts, fileTime{f, st.ModTime()})
		}
	}
	slices.SortFunc(fts, func(a, b fileTime) int { return b.mod.Compare(a.mod) })

	var out []Brief
	for _, ft := range fts {
		if len(out) >= maxBriefs {
			break
		}
		if b, ok := briefOf(ft.path); ok {
			b.Updated = ft.mod
			out = append(out, b)
		}
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
				b.ID, b.Provider, b.Model = r.ID, r.Provider, r.Model
			}
		case bytes.HasPrefix(line, userPrefix):
			b.Turns++
			if b.Prompt == "" {
				var r rawRec
				if json.Unmarshal(line, &r) == nil {
					b.Prompt = oneLine(r.Content, 40)
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
// 其余按会话 ID 前缀匹配。
func Find(sessionsDir, cwd, selector string) (Brief, error) {
	list, err := List(sessionsDir, cwd)
	if err != nil {
		return Brief{}, err
	}
	if len(list) == 0 {
		return Brief{}, errors.New("当前目录下还没有可恢复的会话")
	}
	sel := strings.TrimSpace(selector)
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
