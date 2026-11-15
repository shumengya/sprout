package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStatuslineTemplateIsValidConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "statusline.jsonc")
	created, err := EnsureStatuslineTemplate(p)
	if err != nil || !created {
		t.Fatalf("首次应创建模板，得到 %v, %v", created, err)
	}
	f, err := LoadStatusline(p)
	if err != nil {
		t.Fatalf("模板自己必须能通过 LoadStatusline: %v", err)
	}
	if len(f.Items) == 0 {
		t.Fatal("模板里至少要列出一项")
	}
	for _, it := range f.Items {
		if it.Name == "" {
			t.Errorf("模板里有条目没有 name: %+v", it)
		}
		if it.Line != 0 {
			t.Errorf("模板不该写死行号，留空才能跟随程序的内置布局: %+v", it)
		}
	}
}

func TestEnsureStatuslineTemplateNeverOverwrites(t *testing.T) {
	p := filepath.Join(t.TempDir(), "statusline.jsonc")
	if err := os.WriteFile(p, []byte(`{"items": []}`), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := EnsureStatuslineTemplate(p)
	if err != nil || created {
		t.Fatalf("已有文件不应覆盖，得到 %v, %v", created, err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != `{"items": []}` {
		t.Errorf("内容被改了: %q", b)
	}
}

func TestStatuslineExampleMatchesTemplate(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "statusline.example.jsonc"))
	if err != nil {
		t.Fatal(err)
	}
	if !statuslineTemplateMatches(b) {
		t.Fatal("statusline.example.jsonc 与 config.StatuslineTemplate 不一致，请同步修改")
	}
}

func TestLoadStatuslineReportsSyntaxErrors(t *testing.T) {
	p := filepath.Join(t.TempDir(), "statusline.jsonc")
	if err := os.WriteFile(p, []byte(`{ "items": [ { "name": "路径" } ]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadStatusline(p); err == nil {
		t.Fatal("少一个右括号应报语法错误")
	}
}
