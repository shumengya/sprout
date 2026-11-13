package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Versions 记住每个服务端上次谈成的协议版本。
//
// 只用来省掉 server/discover 的一次试探：现网服务端几乎都只认旧版握手，
// 先试新版再退回，等于每个服务端多一次注定失败的往返。缓存永远不是权威——
// 目标变了、服务端升级了、令牌失效了，握手都会失败并自动退回完整协商。
// 因此读不出来、写不进去一律当作没有缓存，不影响连接。
type Versions struct {
	mu   sync.Mutex
	path string
	seen map[string]entry
}

type entry struct {
	Target  string `json:"target"` // 连接目标的指纹：地址或命令变了就不复用
	Version string `json:"version"`
}

// LoadVersions 读取版本缓存；文件缺失或损坏都返回空缓存，不报错。
func LoadVersions(path string) *Versions {
	v := &Versions{path: path, seen: map[string]entry{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return v
	}
	_ = json.Unmarshal(data, &v.seen)
	if v.seen == nil {
		v.seen = map[string]entry{}
	}
	return v
}

// Lookup 返回这个服务端上次谈成的版本；没有缓存、或连接目标已经变了时返回空串。
func (v *Versions) Lookup(spec Spec) string {
	if v == nil {
		return ""
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	e, ok := v.seen[spec.Name]
	if !ok || e.Target != target(spec) {
		return ""
	}
	return e.Version
}

// Record 记住这次连接的版本。
func (v *Versions) Record(spec Spec, version string) {
	if v == nil || version == "" {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.seen[spec.Name] = entry{Target: target(spec), Version: version}
}

// Save 把缓存写回文件。先写临时文件再改名，避免崩在半路留下半截 JSON。
func (v *Versions) Save() error {
	if v == nil || v.path == "" {
		return nil
	}
	v.mu.Lock()
	data, err := json.MarshalIndent(v.seen, "", "  ")
	v.mu.Unlock()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(v.path), ".mcp-versions-*")
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
	return os.Rename(name, v.path)
}

// target 是缓存指纹：URL 或启动命令 + 参数的 SHA-256。换地址、改参数都会让它变，旧缓存自然失效。
// 存哈希而不是原文：URL 与 args 里可能展开出了令牌，不能明文写进缓存文件。
func target(spec Spec) string {
	raw := spec.URL
	if !spec.IsHTTP() {
		raw = spec.Command + "\x00" + strings.Join(spec.Args, "\x00")
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
