package mcp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func toolsOf(names ...string) []ToolInfo {
	out := make([]ToolInfo, 0, len(names))
	for _, n := range names {
		out = append(out, ToolInfo{Name: n})
	}
	return out
}

func TestToolCacheLookupExpiresAndChecksTarget(t *testing.T) {
	c := LoadTools(filepath.Join(t.TempDir(), "mcp-tools.json"))
	spec := Spec{Name: "s", URL: "https://a/mcp"}

	if _, ok := c.Lookup(spec); ok {
		t.Error("空缓存不该命中")
	}
	c.Record(spec, toolsOf("echo"), time.Minute)
	if got, ok := c.Lookup(spec); !ok || len(got) != 1 || got[0].Name != "echo" {
		t.Errorf("刚写进去的应命中: %+v ok=%v", got, ok)
	}
	// 换了地址：指纹变了，旧缓存不该复用。
	if _, ok := c.Lookup(Spec{Name: "s", URL: "https://b/mcp"}); ok {
		t.Error("连接目标变了不该命中")
	}
	// 过期。
	c.Record(spec, toolsOf("echo"), -time.Second)
	if _, ok := c.Lookup(spec); ok {
		t.Error("过期后不该命中")
	}
}

func TestToolCacheZeroTTLDropsTheEntry(t *testing.T) {
	// 服务端返回 ttlMs: 0 表示不要缓存。
	c := LoadTools(filepath.Join(t.TempDir(), "mcp-tools.json"))
	spec := Spec{Name: "s", URL: "https://a/mcp"}
	c.Record(spec, toolsOf("echo"), time.Minute)
	c.Record(spec, toolsOf("echo"), 0)
	if _, ok := c.Lookup(spec); ok {
		t.Error("ttl 为 0 时应清掉缓存")
	}
}

func TestToolCacheSurvivesSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp-tools.json")
	spec := Spec{Name: "s", URL: "https://a/mcp"}
	c := LoadTools(path)
	c.Record(spec, toolsOf("echo", "read"), time.Hour)
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got, ok := LoadTools(path).Lookup(spec)
	if !ok || len(got) != 2 || got[1].Name != "read" {
		t.Errorf("从文件读回的缓存不对: %+v ok=%v", got, ok)
	}
}

func TestDialUsesTheCacheInsteadOfListingAgain(t *testing.T) {
	// 有效期内第二次启动不该再发 tools/list。
	var calls []string
	srv := legacyServer(t, &calls)
	defer srv.Close()

	cachePath := filepath.Join(t.TempDir(), "mcp-tools.json")
	cache := LoadTools(cachePath)
	spec := Spec{Name: "fake", URL: srv.URL + "/mcp", Cache: cache}

	first, err := Dial(context.Background(), spec, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
	if !strings.Contains(strings.Join(calls, ","), "tools/list") {
		t.Fatal("第一次连接应发 tools/list")
	}
	if err := cache.Save(); err != nil {
		t.Fatal(err)
	}

	calls = nil
	spec.Cache = LoadTools(cachePath) // 模拟下次启动重新读盘
	second, err := Dial(context.Background(), spec, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if got := strings.Join(calls, ","); strings.Contains(got, "tools/list") {
		t.Errorf("缓存有效时不该再发 tools/list: %s", got)
	}
	if tools := second.Tools(); len(tools) != 1 || tools[0].Name != "echo" {
		t.Errorf("应从缓存拿到工具: %+v", tools)
	}
}
