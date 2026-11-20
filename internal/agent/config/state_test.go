package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSettingsExampleMatchesTemplate(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "settings.example.jsonc"))
	if err != nil {
		t.Fatal(err)
	}
	// git 的 autocrlf 可能把示例文件检出成 CRLF，比较时统一换行。
	got := strings.ReplaceAll(string(b), "\r\n", "\n")
	if got != SettingsTemplate {
		t.Fatal("settings.example.jsonc 与 config.SettingsTemplate 不一致，请同步修改")
	}
}

// 保存只改 model 与 effort 两个键：注释、缩进和用户自己加的设置都要留着。
func TestSaveSettingsKeepsCommentsAndOtherKeys(t *testing.T) {
	dir := t.TempDir()
	p := SettingsPath(dir)
	hand := "// 我的备注\n{\n  \"model\": \"old/model\",\n  // 主题，程序不该动\n  \"theme\": \"dark\",\n  \"effort\": \"low\",\n}\n"
	if err := os.WriteFile(p, []byte(hand), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveSettings(p, Settings{Model: "new/model", Effort: "max"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, want := range []string{"// 我的备注", "// 主题，程序不该动", `"theme": "dark"`, `"model": "new/model"`, `"effort": "max"`} {
		if !strings.Contains(text, want) {
			t.Errorf("保存后缺少 %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "old/model") || strings.Contains(text, `"low"`) {
		t.Errorf("旧值应被替换:\n%s", text)
	}
	got, err := LoadSettings(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "new/model" || got.Effort != "max" {
		t.Errorf("读回 = %+v", got)
	}
}

// 文件还不存在时，保存会先补模板再改两个键，注释因此来自模板。
func TestSaveSettingsCreatesFromTemplate(t *testing.T) {
	p := SettingsPath(t.TempDir())
	if err := SaveSettings(p, Settings{Model: "ds/flash", Effort: "high"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "上次使用的模型") {
		t.Errorf("新建的文件应带模板注释:\n%s", b)
	}
	got, err := LoadSettings(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "ds/flash" || got.Effort != "high" {
		t.Errorf("读回 = %+v", got)
	}
}

// 改完解析不了时回滚，不留下坏配置。顶层不是对象就属于这种情况。
func TestSaveSettingsRollsBackOnInvalid(t *testing.T) {
	p := SettingsPath(t.TempDir())
	orig := "[1, 2, 3]\n"
	if err := os.WriteFile(p, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveSettings(p, Settings{Model: "x", Effort: "y"}); err == nil {
		t.Fatal("顶层不是对象应报错")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != orig {
		t.Errorf("失败后应恢复原文，得到:\n%s", b)
	}
}

func TestMigrateStateFile(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "state.json")
	if err := os.WriteFile(old, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := MigrateStateFile(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("迁移后 state.json 应消失：%v", err)
	}
	if _, err := os.Stat(SettingsPath(dir)); err != nil {
		t.Errorf("应生成 settings.jsonc：%v", err)
	}

	// 目标已存在时不动它，旧文件留着。
	if err := os.WriteFile(old, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SettingsPath(dir), []byte("// 手改过\n{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := MigrateStateFile(dir); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(SettingsPath(dir))
	if !strings.Contains(string(b), "手改过") {
		t.Errorf("已有的 settings.jsonc 不该被覆盖：\n%s", b)
	}
	if _, err := os.Stat(old); err != nil {
		t.Errorf("目标已存在时 state.json 应保留：%v", err)
	}

	// 两边都没有时什么也不做。
	if err := MigrateStateFile(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

// 空文件按“还没写过”处理，补上模板而不是当成语法错误。
func TestEnsureSettingsFillsEmptyFile(t *testing.T) {
	p := SettingsPath(t.TempDir())
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	created, err := EnsureSettings(p)
	if err != nil || !created {
		t.Fatalf("空文件应补模板，得到 %v, %v", created, err)
	}
	if _, err := LoadSettings(p); err != nil {
		t.Fatal(err)
	}
	if created, _ := EnsureSettings(p); created {
		t.Fatal("已有内容时不应再写")
	}
}
