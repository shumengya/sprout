package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Fork 把 src 会话的前 turns 轮复制成同一分组下的新会话，返回新会话文件的路径；turns 为 0 表示全部。
// 轮数按 user 记录计数，与会话列表里的轮数一致。原会话不做任何改动。
//
// 截断点在第 turns+1 条 user 记录之前：JSONL 的前缀重放出来就是当时的状态，
// 压缩、裁剪、中断等记录都按原样重放，不需要另外处理。
// 被保留的记录引用的图片复制到新会话自己的 .images 目录，删掉原会话不会让分支丢图。
func Fork(sessionsDir, src string, turns int) (string, error) {
	p, err := inSessions(sessionsDir, src)
	if err != nil {
		return "", err
	}
	if turns < 0 {
		return "", fmt.Errorf("轮数不能为负数: %d", turns)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("读取会话文件失败: %w", err)
	}
	lines := splitLines(raw)
	if len(lines) == 0 || !bytes.HasPrefix(lines[0], sessionPrefix) {
		return "", fmt.Errorf("%s 不是有效的会话文件：缺少 session 头记录", p)
	}
	var head sessionRec
	if err := json.Unmarshal(lines[0], &head); err != nil {
		return "", fmt.Errorf("会话头记录损坏: %w", err)
	}

	total := 0
	for _, l := range lines {
		if bytes.HasPrefix(l, userPrefix) {
			total++
		}
	}
	switch {
	case total == 0:
		return "", errors.New("当前会话还没有任何提问，不需要分支")
	case turns > total:
		return "", fmt.Errorf("会话只有 %d 轮，不能保留前 %d 轮", total, turns)
	case turns == 0:
		turns = total
	}
	keep := lines[1:]
	for i, seen := 0, 0; i < len(keep); i++ {
		if bytes.HasPrefix(keep[i], userPrefix) {
			if seen == turns {
				keep = keep[:i]
				break
			}
			seen++
		}
	}

	dir := filepath.Dir(p)
	id := newID()
	started := time.Now()
	file := fmt.Sprintf("%s_%s.jsonl", started.Format("20060102-150405"), id)
	dst := filepath.Join(dir, file)
	oldImages := strings.TrimSuffix(filepath.Base(p), ".jsonl") + ".images"
	newImages := strings.TrimSuffix(file, ".jsonl") + ".images"

	head.header = header{Type: "session", Time: started}
	head.ID, head.Parent, head.ParentTurns = id, head.ID, turns
	var buf bytes.Buffer
	b, err := json.Marshal(head)
	if err != nil {
		return "", err
	}
	buf.Write(append(b, '\n'))
	// 只认 "file":" 后面紧跟的目录名：正文里出现同样的文字时引号是转义过的（\"），不会被误改。
	oldRef := []byte(`"file":"` + oldImages + `/`)
	newRef := []byte(`"file":"` + newImages + `/`)
	var images []string
	for _, l := range keep {
		if bytes.Contains(l, oldRef) {
			var r rawRec
			if json.Unmarshal(l, &r) == nil {
				for _, img := range r.Images {
					if name, ok := strings.CutPrefix(img.File, oldImages+"/"); ok {
						images = append(images, name)
					}
				}
			}
			l = bytes.ReplaceAll(l, oldRef, newRef)
		}
		buf.Write(l)
		buf.WriteByte('\n')
	}

	if err := copyImages(filepath.Join(dir, oldImages), filepath.Join(dir, newImages), images); err != nil {
		return "", err
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("创建分支会话失败: %w", err)
	}
	_, werr := f.Write(buf.Bytes())
	if err := errors.Join(werr, f.Close()); err != nil {
		// 新文件和图片目录都是这次刚建的，名字里有新 id，不会碰到别的会话。
		os.Remove(dst)
		os.RemoveAll(filepath.Join(dir, newImages))
		return "", fmt.Errorf("写入分支会话失败: %w", err)
	}
	// meta.json 只是索引，登记失败不影响分支本身，和记录器的处理一致。
	_, _ = updateMeta(dir, file, head.Cwd, sessionMeta{
		ID: id, Provider: head.Provider, Model: head.Model, Tools: head.Tools,
		StartedAt: started, Version: head.Version,
	})
	return dst, nil
}

// splitLines 按行切分并去掉空行。最后一行可能是进程被杀时写了一半的记录，原样保留，重放时会被跳过。
func splitLines(raw []byte) [][]byte {
	var out [][]byte
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		if l := bytes.TrimSpace(sc.Bytes()); len(l) > 0 {
			out = append(out, append([]byte(nil), l...))
		}
	}
	return out
}

// copyImages 把 names 从 from 目录复制到 to 目录。原图丢了就跳过：重放时会换成“图片无法恢复”的说明，
// 和原会话的表现一致，不该因此拒绝分支。
func copyImages(from, to string, names []string) error {
	if len(names) == 0 {
		return nil
	}
	if err := os.MkdirAll(to, 0o755); err != nil {
		return fmt.Errorf("创建图片目录失败: %w", err)
	}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(from, name))
		if err != nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(to, name), data, 0o600); err != nil {
			return fmt.Errorf("复制图片失败: %w", err)
		}
	}
	return nil
}
