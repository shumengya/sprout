package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DefaultToolTTL 是服务端没有给出 ttlMs 时工具列表的缓存时长。
// 没有缓存时每次启动都要重新 tools/list，对远程服务端是几十到几百毫秒的额外往返。
const DefaultToolTTL = 10 * time.Minute

// ToolCache 把每个服务端的工具列表落盘，下次启动在有效期内直接复用。
//
// 和版本缓存一样，缓存永远不是权威：可以用「重连」或服务端的 list_changed 通知刷新。
// 指纹按连接目标计算，地址或启动命令变了旧缓存自然失效。
type ToolCache struct {
	mu    sync.Mutex
	path  string
	items map[string]cachedTools
}

type cachedTools struct {
	Target    string     `json:"target"`
	Tools     []ToolInfo `json:"tools"`
	ExpiresAt time.Time  `json:"expiresAt"`
}

// LoadTools 读取工具缓存；文件缺失或损坏都当作空缓存，不报错。
func LoadTools(path string) *ToolCache {
	c := &ToolCache{path: path, items: map[string]cachedTools{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	_ = json.Unmarshal(data, &c.items)
	if c.items == nil {
		c.items = map[string]cachedTools{}
	}
	return c
}

// Lookup 返回仍在有效期内的工具列表。没有缓存、已过期或连接目标变了时 ok 为 false。
func (c *ToolCache) Lookup(spec Spec) ([]ToolInfo, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[spec.Name]
	if !ok || e.Target != target(spec) || time.Now().After(e.ExpiresAt) {
		return nil, false
	}
	return append([]ToolInfo(nil), e.Tools...), true
}

// Record 记住这次拿到的工具列表。ttl <= 0 表示服务端要求不缓存，清掉已有条目。
func (c *ToolCache) Record(spec Spec, tools []ToolInfo, ttl time.Duration) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if ttl <= 0 {
		delete(c.items, spec.Name)
		return
	}
	c.items[spec.Name] = cachedTools{
		Target: target(spec), Tools: append([]ToolInfo(nil), tools...), ExpiresAt: time.Now().Add(ttl),
	}
}

// Drop 清掉某个服务端的缓存，用于连接出错或用户要求刷新。
func (c *ToolCache) Drop(name string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, name)
}

// Save 写回文件。先写临时文件再改名；缓存写不进去不影响功能。
func (c *ToolCache) Save() error {
	if c == nil || c.path == "" {
		return nil
	}
	c.mu.Lock()
	data, err := json.MarshalIndent(c.items, "", "  ")
	c.mu.Unlock()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.path), ".mcp-tools-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, c.path)
}
