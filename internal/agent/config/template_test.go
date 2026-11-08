package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTemplateIsValidConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "models.jsonc")
	created, err := EnsureTemplate(p)
	if err != nil || !created {
		t.Fatalf("首次应创建模板，得到 %v, %v", created, err)
	}
	f, err := Load(p)
	if err != nil {
		t.Fatalf("模板自己必须能通过 Load 和校验: %v", err)
	}
	sel, err := f.Selected()
	if err != nil {
		t.Fatal(err)
	}
	if sel.Ref() != "deepseek/deepseek-flash" || sel.Provider.Protocol != ProtocolChat {
		t.Fatalf("默认选择不对: %+v", sel)
	}
	// 参数来自 DeepSeek 官方文档（模型 & 价格）：上下文 1M，最大输出 384K，支持工具调用。
	if sel.Model.ContextWindow != 1000000 || sel.Model.MaxOutput != 384000 || !sel.Model.SupportsTools() {
		t.Errorf("deepseek-flash 的参数不对: %+v", sel.Model)
	}
	if sel.Provider.BaseURL != "https://api.deepseek.com" {
		t.Errorf("baseUrl = %q", sel.Provider.BaseURL)
	}
	// 模板必须同时展示两种协议。
	protocols := map[string]bool{}
	for _, pr := range f.Providers {
		protocols[pr.Protocol] = true
	}
	if !protocols[ProtocolChat] || !protocols[ProtocolResponses] {
		t.Errorf("模板应覆盖两种协议: %v", protocols)
	}
}

func TestEnsureTemplateNeverOverwrites(t *testing.T) {
	p := filepath.Join(t.TempDir(), "models.jsonc")
	if err := os.WriteFile(p, []byte("用户自己的内容"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := EnsureTemplate(p)
	if err != nil || created {
		t.Fatalf("已有文件不应覆盖，得到 %v, %v", created, err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "用户自己的内容" {
		t.Fatalf("文件被改动: %q", b)
	}
}

func TestEnsureTemplateSecondCallKeepsEdits(t *testing.T) {
	p := filepath.Join(t.TempDir(), "models.jsonc")
	if _, err := EnsureTemplate(p); err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(Template, `"apiKey": ""`, `"apiKey": "sk-test"`, 1)
	if err := os.WriteFile(p, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	if created, _ := EnsureTemplate(p); created {
		t.Fatal("第二次调用不应再创建")
	}
	if b, _ := os.ReadFile(p); string(b) != edited {
		t.Fatal("用户填写的 apiKey 被覆盖了")
	}
}

func TestEnsureTemplateFailsWhenDirMissing(t *testing.T) {
	if _, err := EnsureTemplate(filepath.Join(t.TempDir(), "missing", "models.jsonc")); err == nil {
		t.Fatal("目录不存在应返回错误")
	}
}

// 防止内置模板与仓库根目录的示例文件各改各的。
func TestTemplateMatchesExampleFile(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "models.example.jsonc"))
	if err != nil {
		t.Fatal(err)
	}
	// git 的 autocrlf 可能把示例文件检出成 CRLF，比较时统一换行。
	got := strings.ReplaceAll(string(b), "\r\n", "\n")
	if got != Template {
		t.Fatal("models.example.jsonc 与 config.Template 不一致，请同步修改")
	}
}
