package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tailscale/hujson"
)

// writeMCP 写一份带注释的 mcp.jsonc，返回路径。
func writeMCP(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mcp.jsonc")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const commentedMCP = `{
  // 顶层说明
  "servers": {
    // github 服务端
    "github": {
      "enable": true, // 开着
      "url": "https://x/mcp"
    },
    "fs": {
      // 本地文件
      "command": "npx",
      "args": ["-y", "fs"]
    }
  }
}
`

func TestSetMCPServerEnabledKeepsComments(t *testing.T) {
	path := writeMCP(t, commentedMCP)
	if err := SetMCPServerEnabled(path, "github", false); err != nil {
		t.Fatal(err)
	}
	// 给没有 enable 的服务端补一个：插在最后一个成员之后，缩进跟上同一层。
	if err := SetMCPServerEnabled(path, "fs", true); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, want := range []string{"// 顶层说明", "// github 服务端", "// 本地文件", "\"enable\": false"} {
		if !strings.Contains(text, want) {
			t.Errorf("缺少 %q:\\n%s", want, text)
		}
	}
	// 注释与原字段原样保留，没有被重新序列化。
	if !strings.Contains(text, "\"enable\": true, // 开着") && !strings.Contains(text, "\"enable\": false") {
		t.Errorf("原行的注释应留在原处:\\n%s", text)
	}

	f, err := LoadMCP(path)
	if err != nil {
		t.Fatalf("改完的配置必须仍能解析: %v\\n%s", err, text)
	}
	if f.Servers["github"].Enable {
		t.Error("github 应已停用")
	}
	if !f.Servers["fs"].Enable {
		t.Error("fs 应已启用")
	}
	if got := strings.Join(f.Servers["fs"].Args, ","); got != "-y,fs" {
		t.Errorf("新增键不该破坏相邻字段: args = %q", got)
	}
}

func TestSetMCPServerEnabledPreservesBOMAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.jsonc")
	content := "\xEF\xBB\xBF{\"servers\": {\"x\": {\"url\": \"https://x/mcp\"}}}"
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := SetMCPServerEnabled(path, "x", true); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(b), "\xEF\xBB\xBF") {
		t.Error("BOM 应原样保留")
	}
	// Windows 上 Go 报告的权限位恒为 0666，这里只在能表达权限的平台上检查。
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o640 {
			t.Errorf("权限应保留，得到 %v", info.Mode().Perm())
		}
	}
}

func TestSetMCPServerEnabledRejectsInvalidResult(t *testing.T) {
	// 没有 url / command 的服务端启用后配置不合法：写回要被撤回。
	path := writeMCP(t, `{"servers": {"empty": {"enable": false}}}`)
	before, _ := os.ReadFile(path)
	if err := SetMCPServerEnabled(path, "empty", true); err == nil {
		t.Fatal("应报错：启用后没有 url / command")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Errorf("不合法时不该留下改动:\\n%s", after)
	}
}

func TestSetMCPServerEnabledUnknownServer(t *testing.T) {
	path := writeMCP(t, commentedMCP)
	if err := SetMCPServerEnabled(path, "nope", true); err == nil {
		t.Fatal("不存在的服务端应报错")
	}
}

func TestSetMCPToolExcluded(t *testing.T) {
	path := writeMCP(t, commentedMCP)
	if err := SetMCPToolExcluded(path, "github", "write_file", true); err != nil {
		t.Fatal(err)
	}
	f, _ := LoadMCP(path)
	if !f.Servers["github"].IsExcluded("write_file") {
		t.Fatalf("应写进 excludeTools: %+v", f.Servers["github"].ExcludeTools)
	}

	// 关掉再打开：从 excludeTools 里移走，列表空了就写回空数组，键还在。
	if err := SetMCPToolExcluded(path, "github", "write_file", false); err != nil {
		t.Fatal(err)
	}
	f, _ = LoadMCP(path)
	if f.Servers["github"].IsExcluded("write_file") {
		t.Error("应已移出 excludeTools")
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "\"excludeTools\": []") {
		t.Errorf("空列表也要留下键:\\n%s", b)
	}

	// 大小写不敏感：已存在的同名项（大小写不同）不会被重复写入。
	if err := SetMCPToolExcluded(path, "github", "Write_File", true); err != nil {
		t.Fatal(err)
	}
	if err := SetMCPToolExcluded(path, "github", "write_file", true); err != nil {
		t.Fatal(err)
	}
	f, _ = LoadMCP(path)
	if got := len(f.Servers["github"].ExcludeTools); got != 1 {
		t.Errorf("不该重复写入，得到 %v", f.Servers["github"].ExcludeTools)
	}
}

// 单行对象里补一个键：就地追加，不换行破坏排版。
func TestInsertMemberInline(t *testing.T) {
	const src = `{"servers": {"a": {"url": "https://x/mcp"}}}`
	v, err := hujson.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	out := string(insertMember([]byte(src), v.Find("/servers/a"), "enable", "true"))
	if want := `{"servers": {"a": {"url": "https://x/mcp", "enable": true}}}`; out != want {
		t.Errorf("单行插入结果 = %s，期望 %s", out, want)
	}
}

// 空对象里插入第一个成员：多行对象要换行并跟上缩进，不能挤成坏结构。
func TestInsertMemberIntoEmptyObject(t *testing.T) {
	const src = "{\n  \"servers\": {\n    \"x\": {}\n  }\n}\n"
	v, _ := hujson.Parse([]byte(src))
	out := string(insertMember([]byte(src), v.Find("/servers/x"), "enable", "false"))
	got, err := hujson.Parse([]byte(out))
	if err != nil {
		t.Fatalf("插入后不是合法 JSONC: %v\n%s", err, out)
	}
	if lit, ok := got.Find("/servers/x/enable").Value.(hujson.Literal); !ok || string(lit) != "false" {
		t.Errorf("没插进去:\n%s", out)
	}
}
