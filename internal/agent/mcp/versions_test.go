package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionsRoundTripAndTargetInvalidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp-versions.json")
	v := LoadVersions(path)
	spec := Spec{Name: "srv", URL: "https://example.com/mcp?key=SECRET"}
	if got := v.Lookup(spec); got != "" {
		t.Fatalf("没有缓存时应为空: %q", got)
	}
	v.Record(spec, ProtocolLegacy)
	if got := LoadVersions(path).Lookup(spec); got != "" {
		t.Fatalf("还没保存就能读到了: %q", got)
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}

	if got := LoadVersions(path).Lookup(spec); got != ProtocolLegacy {
		t.Errorf("保存后读回 = %q", got)
	}
	// 换了地址就不该复用：那可能是另一个服务端。
	if got := LoadVersions(path).Lookup(Spec{Name: "srv", URL: "https://other.com/mcp?key=SECRET"}); got != "" {
		t.Errorf("目标变了仍命中缓存: %q", got)
	}

	// 指纹是哈希：URL 里的令牌不能明文落盘。
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "SECRET") {
		t.Errorf("缓存文件泄露了 URL 里的令牌: %s", data)
	}
}

func TestLoadVersionsToleratesBrokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp-versions.json")
	if err := os.WriteFile(path, []byte("{不是 JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LoadVersions(path).Lookup(Spec{Name: "srv", URL: "https://x/mcp"}); got != "" {
		t.Errorf("坏文件应当作没有缓存: %q", got)
	}
}

// 记住了旧版版本后，握手应直接 initialize，不再先试 server/discover。
func TestDialKnownLegacyVersionSkipsDiscover(t *testing.T) {
	var calls []string
	srv := legacyServer(t, &calls)
	defer srv.Close()

	c, err := Dial(context.Background(), Spec{
		Name: "fake", URL: srv.URL + "/mcp", KnownVersion: ProtocolLegacy,
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if got := strings.Join(calls, ","); got != "initialize,notifications/initialized,tools/list" {
		t.Errorf("应跳过 server/discover，实际顺序: %s", got)
	}
	if c.Version() != ProtocolLegacy {
		t.Errorf("版本 = %q", c.Version())
	}
}

// 缓存的版本已经过时（服务端升级或换实现）时，要自动退回完整协商，而不是连不上。
func TestDialStaleKnownVersionFallsBack(t *testing.T) {
	var calls []string
	srv := legacyServer(t, &calls)
	defer srv.Close()

	c, err := Dial(context.Background(), Spec{
		Name: "fake", URL: srv.URL + "/mcp", KnownVersion: ProtocolStateless,
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if c.Version() != ProtocolLegacy {
		t.Errorf("退回后应谈成旧版，实际 %q；顺序: %s", c.Version(), strings.Join(calls, ","))
	}
	if len(c.Tools()) != 1 {
		t.Errorf("工具 = %+v", c.Tools())
	}
}
