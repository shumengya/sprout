package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPTemplateIsValidAndEmpty(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mcp.jsonc")
	created, err := EnsureMCPTemplate(p)
	if err != nil || !created {
		t.Fatalf("首次应创建模板，得到 %v, %v", created, err)
	}
	f, err := LoadMCP(p)
	if err != nil {
		t.Fatalf("模板自己必须能通过 LoadMCP 和校验: %v", err)
	}
	if len(f.Servers) != 0 {
		t.Errorf("模板里不该有启用的服务端，得到 %v", f.EnabledServers())
	}
}

func TestMCPExampleMatchesTemplate(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "mcp.example.jsonc"))
	if err != nil {
		t.Fatal(err)
	}
	if !mcpTemplateMatches(b) {
		t.Fatal("mcp.example.jsonc 与 config.MCPTemplate 不一致，请同步修改")
	}
}

func TestLoadMCPMissingFileIsEmptyNotError(t *testing.T) {
	f, err := LoadMCP(filepath.Join(t.TempDir(), "mcp.jsonc"))
	if err != nil {
		t.Fatalf("缺文件是常态，不该报错: %v", err)
	}
	if len(f.Servers) != 0 {
		t.Errorf("Servers = %v", f.Servers)
	}
}

func TestLoadMCPValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.jsonc")
	content := `{
	  "servers": {
	    "both": { "enable": true, "url": "https://x/mcp", "command": "npx" },
	    "empty": { "enable": true },
	    "bad-url": { "enable": true, "url": "ftp://x" },
	    "space-cmd": { "enable": true, "command": "npx -y pkg" },
	    "neg-timeout": { "enable": true, "url": "https://x/mcp", "timeoutSeconds": -1 },
	    "bad name": { "enable": true, "url": "https://x/mcp" },
	    "excluded": { "enable": true, "url": "https://x/mcp", "excludeTools": ["a", ""] }
	  }
	}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadMCP(path)
	if err == nil {
		t.Fatal("应报出全部问题")
	}
	msg := err.Error()
	for _, want := range []string{
		"servers.both 不能同时写 url 和 command",
		"servers.empty 已启用，但没有写 url 或 command",
		"servers.bad-url.url",
		"servers.space-cmd.command 包含空格",
		"servers.neg-timeout.timeoutSeconds 不能为负数",
		"不是合法的服务端名",
		"servers.excluded.excludeTools 里有空字符串",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("缺少 %q:\n%s", want, msg)
		}
	}
}

func TestMCPEnabledServersSortedAndExcluded(t *testing.T) {
	f := &MCPFile{Servers: map[string]MCPServer{
		"b": {Enable: true, URL: "https://x/mcp"},
		"a": {Enable: true, Command: "node"},
		"c": {URL: "https://x/mcp"},
	}}
	if got := strings.Join(f.EnabledServers(), ","); got != "a,b" {
		t.Errorf("EnabledServers = %q", got)
	}
	s := MCPServer{ExcludeTools: []string{" Foo ", "bar"}}
	if !s.IsExcluded("foo") || !s.IsExcluded("BAR") || s.IsExcluded("baz") {
		t.Error("excludeTools 应大小写不敏感且忽略空白")
	}
}

func TestEnsureMCPTemplateKeepsExisting(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mcp.jsonc")
	custom := `{"servers": {"x": {"enable": true, "url": "https://x/mcp"}}}`
	if err := os.WriteFile(p, []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := EnsureMCPTemplate(p)
	if err != nil || created {
		t.Fatalf("已有内容时不该覆盖，得到 %v, %v", created, err)
	}
	if b, _ := os.ReadFile(p); string(b) != custom {
		t.Errorf("内容被改了: %s", b)
	}
}
