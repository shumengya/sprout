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
// 它是汇总信息，各会话 JSONL 开头的 session 记录才是权威来源；唯一只存在这里的是用户起的名字。
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
	// Name 是用户在 /resume 里起的名字。
	Name string `json:"name,omitempty"`
}

// readMeta 读分组目录的 meta.json。读不到或损坏时返回空索引：它只是索引，列表照样能从 JSONL 生成。
func readMeta(groupDir string) groupMeta {
	gm := groupMeta{Sessions: map[string]sessionMeta{}}
	raw, err := os.ReadFile(filepath.Join(groupDir, "meta.json"))
	if err != nil {
		return gm
	}
	var old groupMeta
	if json.Unmarshal(raw, &old) == nil {
		gm.Cwd = old.Cwd
		if old.Sessions != nil {
			gm.Sessions = old.Sessions
		}
	}
	return gm
}

// updateMeta 把本次会话登记进分组目录的 meta.json。已有登记（比如恢复后写入）保留用户起的名字。
func updateMeta(groupDir, file string, cwd string, sm sessionMeta) (warn string, err error) {
	return editMetaWarn(groupDir, func(gm *groupMeta) {
		gm.Cwd = cwd
		sm.Name = gm.Sessions[file].Name
		gm.Sessions[file] = sm
	})
}

func editMeta(groupDir string, fn func(*groupMeta)) error {
	_, err := editMetaWarn(groupDir, fn)
	return err
}

// editMetaWarn 对 meta.json 做读-改-写。
// 不加跨进程锁：两个 sprout 实例在同一目录同时写时可能丢失其中一次修改，
// 但会话 JSONL 本身不受影响，因此只做原子替换（临时文件 + rename），保证文件永远是完整 JSON。
func editMetaWarn(groupDir string, fn func(*groupMeta)) (warn string, err error) {
	path := filepath.Join(groupDir, "meta.json")
	gm := groupMeta{Sessions: map[string]sessionMeta{}}

	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		var old groupMeta
		if json.Unmarshal(raw, &old) == nil {
			gm.Cwd = old.Cwd
			if old.Sessions != nil {
				gm.Sessions = old.Sessions
			}
		} else {
			warn = "meta.json 已损坏，已重建索引"
		}
	case !errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("读取 meta.json 失败: %w", err)
	}
	fn(&gm)

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
