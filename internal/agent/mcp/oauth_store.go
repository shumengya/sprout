package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AuthStore 把 OAuth 令牌存在一个 JSON 文件里（权限 0600），按服务端名索引。
// 令牌是明文凭证，所以只放这个文件：不进日志，也不进 mcp-versions.json 这类缓存。
type AuthStore struct {
	path string
	mu   sync.Mutex
}

// NewAuthStore 返回读写 path 的令牌存储。文件不存在视为没有任何登录。
func NewAuthStore(path string) *AuthStore { return &AuthStore{path: path} }

// authEntry 是一个服务端的登录状态。URL 用来发现配置改过：换了端点就不能再用旧令牌。
type authEntry struct {
	URL           string    `json:"url"`
	Issuer        string    `json:"issuer"`
	TokenEndpoint string    `json:"tokenEndpoint"`
	ClientID      string    `json:"clientId"`
	AccessToken   string    `json:"accessToken"`
	RefreshToken  string    `json:"refreshToken,omitempty"`
	Expiry        time.Time `json:"expiry,omitzero"`
	Scope         string    `json:"scope,omitempty"`
}

type authFile struct {
	Servers map[string]authEntry `json:"servers"`
}

// load 读文件。内容损坏时报错而不是当作空：静默丢掉用户的登录状态比提示更糟。
func (s *AuthStore) load() (authFile, error) {
	var f authFile
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return authFile{Servers: map[string]authEntry{}}, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return f, fmt.Errorf("%s 不是合法的 JSON，删除它后重新登录: %w", s.path, err)
	}
	if f.Servers == nil {
		f.Servers = map[string]authEntry{}
	}
	return f, nil
}

// get 返回 name 的登录状态；URL 与登录时不同则视为没登录过。
func (s *AuthStore) get(name, url string) (authEntry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return authEntry{}, false, err
	}
	e, ok := f.Servers[name]
	if !ok || e.URL != url {
		return authEntry{}, false, nil
	}
	return e, true, nil
}

// update 读出整份文件，交给 fn 修改后写回。
func (s *AuthStore) update(fn func(map[string]authEntry)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return err
	}
	fn(f.Servers)
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	// 先写临时文件再改名：进程在写到一半时被杀，不会留下半截的令牌文件。
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Put 保存登录状态。
func (s *AuthStore) put(name string, e authEntry) error {
	return s.update(func(m map[string]authEntry) { m[name] = e })
}

// Logout 删除 name 的登录状态，返回之前是否登录过。
func (s *AuthStore) Logout(name string) (bool, error) {
	var had bool
	err := s.update(func(m map[string]authEntry) {
		_, had = m[name]
		delete(m, name)
	})
	return had, err
}

// LoggedIn 报告 name 在 url 上是否有已保存的登录状态（不检查令牌是否过期）。
func (s *AuthStore) LoggedIn(name, url string) bool {
	_, ok, _ := s.get(name, url)
	return ok
}
