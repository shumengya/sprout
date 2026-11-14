package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkills(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "skills.jsonc")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// 模板本身要能解析：用户照抄示例时不该先撞上一堆报错。
func TestSkillsTemplateParses(t *testing.T) {
	f, err := LoadSkills(writeSkills(t, SkillsTemplate))
	if err != nil {
		t.Fatalf("模板解析失败: %v\n%s", err, SkillsTemplate)
	}
	if len(f.Skills) != 0 {
		t.Errorf("模板不该注册任何技能: %+v", f.Skills)
	}
	if !f.ScansAgents() {
		t.Error("模板默认应开启 scanAgents")
	}
}

func TestSetSkillEnabledKeepsComments(t *testing.T) {
	path := writeSkills(t, `{
  // 我的技能
  "skills": {
    "a": {"path": "D:/a", "enable": true}, // 常用
    "b": {"path": "D:/b", "enable": true}
  },
  "scanAgents": false
}
`)
	if err := SetSkillEnabled(path, "a", false); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	text := string(b)
	for _, want := range []string{"// 我的技能", "// 常用", `"scanAgents": false`, `"b"`} {
		if !strings.Contains(text, want) {
			t.Errorf("缺少 %q:\n%s", want, text)
		}
	}
	f, _ := LoadSkills(path)
	if f.Skills["a"].Enable {
		t.Error("a 应已停用")
	}
	if !f.Skills["b"].Enable || f.Skills["b"].Path != "D:/b" {
		t.Errorf("b 不该被改动: %+v", f.Skills["b"])
	}
}

func TestSetSkillEnabledUnknownSkill(t *testing.T) {
	path := writeSkills(t, `{"skills": {"a": {"path": "D:/a", "enable": true}}}`)
	if err := SetSkillEnabled(path, "nope", false); err == nil {
		t.Fatal("未注册的技能应报错")
	}
}

func TestSetScanAgentsWritesKey(t *testing.T) {
	path := writeSkills(t, `{
  // 保留我
  "skills": {}
}
`)
	if err := SetScanAgents(path, false); err != nil {
		t.Fatal(err)
	}
	f, _ := LoadSkills(path)
	if f.ScansAgents() {
		t.Error("应已关闭")
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "// 保留我") || !strings.Contains(string(b), `"scanAgents": false`) {
		t.Errorf("应插入 scanAgents 且保留注释:\n%s", b)
	}
	// 再打开：改回 true，不重复插入。
	if err := SetScanAgents(path, true); err != nil {
		t.Fatal(err)
	}
	f, _ = LoadSkills(path)
	if !f.ScansAgents() {
		t.Error("应已开启")
	}
	if n := strings.Count(string(mustRead(t, path)), "scanAgents"); n != 1 {
		t.Errorf("scanAgents 出现 %d 次:\n%s", n, mustRead(t, path))
	}
}

// 缺 scanAgents 的旧配置视为开启。
func TestScansAgentsDefaultsOn(t *testing.T) {
	f, err := LoadSkills(writeSkills(t, `{"skills": {}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !f.ScansAgents() {
		t.Error("没写 scanAgents 时应视为开启")
	}
}

func TestAddSkillRegistersOnce(t *testing.T) {
	path := writeSkills(t, `{
  // 注册表
  "skills": {}
}
`)
	dir := filepath.Join(t.TempDir(), "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ok, err := AddSkill(path, "demo", dir)
	if err != nil || !ok {
		t.Fatalf("应写入: %v / %v", ok, err)
	}
	ok, err = AddSkill(path, "demo", dir)
	if err != nil || ok {
		t.Fatalf("重复注册应跳过: %v / %v", ok, err)
	}

	f, err := LoadSkills(path)
	if err != nil {
		t.Fatalf("注册后的文件要合法: %v\n%s", err, mustRead(t, path))
	}
	e := f.Skills["demo"]
	if !e.Enable || !strings.Contains(strings.ToLower(e.Path), "demo") {
		t.Errorf("注册内容 = %+v", e)
	}
	if !strings.Contains(mustRead(t, path), "// 注册表") {
		t.Errorf("注释应保留:\n%s", mustRead(t, path))
	}
}

// 文件里没有 skills 对象时先补一个再插。
func TestAddSkillCreatesSkillsObject(t *testing.T) {
	path := writeSkills(t, `{
  "scanAgents": true
}
`)
	if _, err := AddSkill(path, "demo", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	f, err := LoadSkills(path)
	if err != nil || len(f.Skills) != 1 {
		t.Fatalf("注册表 = %+v / %v\n%s", f, err, mustRead(t, path))
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
