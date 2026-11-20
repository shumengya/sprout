package session

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// legacyNames 读旧版本留下的 meta.json，返回其中用户起的名字（键是 JSONL 文件名）。
//
// 旧版本把一个分组下所有会话的索引写在 meta.json 里，名字只存在那里。现在会话的元数据都在
// 各自的 JSONL 里（session 头记录、name 记录），这个文件不再写入，也不删除；
// 只在会话文件里没有 name 记录时，从这里取旧版本起的名字。读不到或损坏时返回 nil。
func legacyNames(groupDir string) map[string]string {
	raw, err := os.ReadFile(filepath.Join(groupDir, "meta.json"))
	if err != nil {
		return nil
	}
	var gm struct {
		Sessions map[string]struct {
			Name string `json:"name"`
		} `json:"sessions"`
	}
	if json.Unmarshal(raw, &gm) != nil {
		return nil
	}
	out := map[string]string{}
	for file, sm := range gm.Sessions {
		if sm.Name != "" {
			out[file] = sm.Name
		}
	}
	return out
}
