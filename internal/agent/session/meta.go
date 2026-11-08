package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// groupMeta 是 meta.json 的内容：一个工作目录分组下所有会话的索引。
// 它是汇总信息，各会话 JSONL 开头的 session 记录才是权威来源。
type groupMeta struct {
	Cwd      string                 `json:"cwd"`
	Sessions map[string]sessionMeta `json:"sessions"` // 键是 JSONL 文件名
}

type sessionMeta struct {
	ID        string    `json:"id"`
	Provider  string    `json:"provider"`
	Model     string    `json:"model"`
	Tools     []string  `json:"tools"`
	StartedAt time.Time `json:"startedAt"`
	Version   string    `json:"version,omitempty"`
}

// updateMeta 把本次会话登记进分组目录的 meta.json。
// 读-改-写不加跨进程锁：两个 sprout 实例在同一目录同时启动时可能丢失其中一条登记，
// 但会话 JSONL 本身不受影响，因此只做原子替换（临时文件 + rename），保证文件永远是完整 JSON。
func updateMeta(groupDir, file string, cwd string, sm sessionMeta) (warn string, err error) {
	path := filepath.Join(groupDir, "meta.json")
	gm := groupMeta{Cwd: cwd, Sessions: map[string]sessionMeta{}}

	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		var old groupMeta
		if json.Unmarshal(raw, &old) == nil {
			if old.Sessions != nil {
				gm.Sessions = old.Sessions
			}
		} else {
			warn = "meta.json 已损坏，已重建索引"
		}
	case !errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("读取 meta.json 失败: %w", err)
	}
	gm.Sessions[file] = sm

	out, err := json.MarshalIndent(gm, "", "  ")
	if err != nil {
		return warn, err
	}
	tmp, err := os.CreateTemp(groupDir, "meta-*.tmp")
	if err != nil {
		return warn, fmt.Errorf("写入 meta.json 失败: %w", err)
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(append(out, '\n'))
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(tmpName)
		return warn, fmt.Errorf("写入 meta.json 失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return warn, fmt.Errorf("替换 meta.json 失败: %w", err)
	}
	return warn, nil
}
