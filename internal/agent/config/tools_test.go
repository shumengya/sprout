package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolsTemplateEnablesEverything(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tools.jsonc")
	created, err := EnsureToolsTemplate(p)
	if err != nil || !created {
		t.Fatalf("首次应创建模板，得到 %v, %v", created, err)
	}
	f, err := LoadTools(p)
	if err != nil {
		t.Fatalf("模板自己必须能通过 LoadTools 和校验: %v", err)
	}
	for _, name := range KnownTools {
		if !f.Enabled(name) {
			t.Errorf("模板里 %s 应默认启用", name)
		}
	}
	if got := strings.Join(f.Names(), ","); got != strings.Join(KnownTools, ",")+",ls" {
		t.Errorf("Names = %s", got)
	}
}

func TestEnsureToolsTemplateReplacesEmptyFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tools.jsonc")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := EnsureToolsTemplate(p)
	if err != nil || !created {
		t.Fatalf("空文件应被替换成模板，得到 %v, %v", created, err)
	}
	if _, err := LoadTools(p); err != nil {
		t.Fatalf("替换后应可解析: %v", err)
	}
}

func TestEnsureToolsTemplateKeepsEdits(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tools.jsonc")
	if _, err := EnsureToolsTemplate(p); err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(ToolsTemplate, `"write": { "enable": true }`, `"write": { "enable": false }`, 1)
	if err := os.WriteFile(p, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	if created, _ := EnsureToolsTemplate(p); created {
		t.Fatal("已有内容不应被覆盖")
	}
	b, _ := os.ReadFile(p)
	if string(b) != edited {
		t.Fatal("用户的修改被覆盖了")
	}
}

func TestLoadToolsHonorsEnable(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tools.jsonc")
	// 同时覆盖：关闭、缺省字段、JSONC 注释与尾逗号。
	content := `{
  "tools": {
    "bash": { "enable": false }, // 关掉
    "read": {},
    "write": { "enable": true },
  },
}`
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := LoadTools(p)
	if err != nil {
		t.Fatal(err)
	}
	if f.Enabled(ToolBash) {
		t.Error("enable:false 应视为关闭")
	}
	if f.Enabled(ToolRead) {
		t.Error("没写 enable 应视为关闭")
	}
	if !f.Enabled(ToolWrite) {
		t.Error("enable:true 应启用")
	}
	if f.Enabled(ToolPowerShell) {
		t.Error("没出现的工具应视为未启用")
	}
	if got := strings.Join(f.EnabledNames(), ","); got != ToolWrite {
		t.Errorf("EnabledNames = %q", got)
	}
}

func TestLoadToolsRejectsUnknownName(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tools.jsonc")
	if err := os.WriteFile(p, []byte(`{"tools": {"red": {"enable": true}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadTools(p)
	if err == nil {
		t.Fatal("未知工具名应报错，避免拼写错误静默失效")
	}
	for _, want := range []string{"red", ToolBash, ToolRead} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息缺少 %q: %v", want, err)
		}
	}
}

func TestLoadToolsReportsSyntaxPosition(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tools.jsonc")
	if err := os.WriteFile(p, []byte("{\n  \"tools\": {\n    oops\n  }\n}"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadTools(p)
	pe, ok := err.(*ParseError)
	if !ok {
		t.Fatalf("应返回 *ParseError，得到 %T: %v", err, err)
	}
	if pe.Line != 3 {
		t.Errorf("行号 = %d，期望 3", pe.Line)
	}
}

func TestToolsPathUnderDataDir(t *testing.T) {
	got := ToolsPath(filepath.Join("data", ".sprout"))
	if filepath.Base(got) != "tools.jsonc" {
		t.Errorf("ToolsPath = %q", got)
	}
}

// 模板必须与仓库根目录的示例文件一致，避免两处各改各的。
func TestToolsTemplateMatchesExampleFile(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "tools.example.jsonc"))
	if err != nil {
		t.Fatal(err)
	}
	if !toolsTemplateMatches(b) {
		t.Fatal("tools.example.jsonc 与 config.ToolsTemplate 不一致，请同步修改")
	}
}

// loadToolsText 把内容写进临时文件并按 JSONC 规则加载。
func loadToolsText(t *testing.T, content string) (*ToolsFile, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tools.jsonc")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return LoadTools(p)
}

func TestExternalToolDefinition(t *testing.T) {
	f, err := loadToolsText(t, `{
  "tools": {
    "ls": {
      "enable": true,
      "description": "列出目录",
      "parameters": {"type": "object", "properties": {"path": {"type": "string"}}, "required": ["path"]},
      "exec": {"shell": "bash", "command": "ls -la {{path}}", "timeoutSeconds": 30}
    },
    "git-status": {
      "enable": true,
      "description": "看状态",
      "exec": {"shell": "direct", "command": "git", "args": ["status", "--short"]}
    }
  }
}`)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := f.Entry("ls")
	if !ok || e.Exec == nil {
		t.Fatal("ls 应带 exec")
	}
	if e.Exec.Shell != ExecBash || e.Exec.Command != "ls -la {{path}}" || e.Exec.TimeoutSeconds != 30 {
		t.Errorf("exec = %+v", e.Exec)
	}
	// 内置工具在前，外部工具按名字排序在后。
	if got := strings.Join(f.Names(), ","); got != "git-status,ls" {
		t.Errorf("Names = %q", got)
	}
}

func TestExternalToolValidation(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"缺少 exec", `{"tools":{"foo":{"enable":true,"description":"d"}}}`,
			"必须提供 exec"},
		{"内置工具不能带 exec", `{"tools":{"bash":{"enable":true,"exec":{"shell":"bash","command":"x"}}}}`,
			"是内置工具"},
		{"缺少描述", `{"tools":{"foo":{"enable":true,"exec":{"shell":"bash","command":"x"}}}}`,
			"description 不能为空"},
		{"shell 名不合法", `{"tools":{"foo":{"enable":true,"description":"d","exec":{"shell":"cmd","command":"x"}}}}`,
			"shell 必须是"},
		{"缺少命令", `{"tools":{"foo":{"enable":true,"description":"d","exec":{"shell":"bash"}}}}`,
			"command 不能为空"},
		{"args 用在 shell 模式", `{"tools":{"foo":{"enable":true,"description":"d","exec":{"shell":"bash","command":"x","args":["y"]}}}}`,
			"只在 shell 为"},
		{"超时过大", `{"tools":{"foo":{"enable":true,"description":"d","exec":{"shell":"bash","command":"x","timeoutSeconds":900}}}}`,
			"不能超过 600"},
		{"参数不是对象 schema", `{"tools":{"foo":{"enable":true,"description":"d","parameters":{"type":"string"},"exec":{"shell":"bash","command":"x"}}}}`,
			"必须是 \"object\""},
		{"工具名不合法", `{"tools":{"1bad":{"enable":true,"description":"d","exec":{"shell":"bash","command":"x"}}}}`,
			"不是合法的工具名"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := loadToolsText(t, c.content)
			if err == nil {
				t.Fatal("应报错")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("错误信息应含 %q，实际: %v", c.want, err)
			}
		})
	}
}

// 内置工具可以覆盖描述与参数，不需要 exec。
func TestBuiltinOverrideIsAllowed(t *testing.T) {
	f, err := loadToolsText(t, `{
  "tools": {
    "read": {
      "enable": true,
      "description": "只读文本",
      "parameters": {"type": "object", "properties": {"path": {"type": "string"}}, "required": ["path"]}
    }
  }
}`)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := f.Entry(ToolRead)
	if e.Description != "只读文本" || len(e.Parameters) == 0 {
		t.Errorf("覆盖值没保存: %+v", e)
	}
}
