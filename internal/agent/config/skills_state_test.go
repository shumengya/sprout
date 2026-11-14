package config

import (
	"os"
	"strings"
	"testing"
)

func TestSkillStateRoundTrip(t *testing.T) {
	path := SkillsStatePath(t.TempDir())
	// 文件不存在时是空状态，不报错。
	st, err := LoadSkillState(path)
	if err != nil || st.IsDisabled("demo") {
		t.Fatalf("缺文件应视为全开: %v / %+v", err, st)
	}

	// 第一次停用会把带注释的模板建出来。
	if err := SetSkillDisabled(path, "demo", true); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !strings.Contains(text, "// 技能开关") || !strings.Contains(text, `"demo"`) {
		t.Errorf("应生成带说明的模板并写入技能名:\n%s", text)
	}
	st, _ = LoadSkillState(path)
	if !st.IsDisabled("demo") || st.IsDisabled("other") {
		t.Errorf("开关状态 = %+v", st)
	}

	// 名字大小写不同也算同一个，不会重复写入。
	if err := SetSkillDisabled(path, "DEMO", true); err != nil {
		t.Fatal(err)
	}
	st, _ = LoadSkillState(path)
	if len(st.Disabled) != 1 {
		t.Errorf("不该重复写入: %v", st.Disabled)
	}

	// 启用：从列表里移走，键留着，成了空数组。
	if err := SetSkillDisabled(path, "demo", false); err != nil {
		t.Fatal(err)
	}
	st, _ = LoadSkillState(path)
	if st.IsDisabled("demo") || len(st.Disabled) != 0 {
		t.Errorf("应已启用: %+v", st)
	}
	b, _ = os.ReadFile(path)
	if !strings.Contains(string(b), `"disabled": []`) {
		t.Errorf("空列表也要留下键:\n%s", b)
	}
}

// 已有的注释与手写排版要保留；只改 disabled 这一项。
func TestSetSkillDisabledKeepsOtherContent(t *testing.T) {
	path := writeMCP(t, `{
  // 我的技能开关
  "disabled": ["a"],
  "note": "keep me"
}
`)
	if err := SetSkillDisabled(path, "b", true); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	text := string(b)
	for _, want := range []string{"// 我的技能开关", `"note": "keep me"`, `"a"`, `"b"`} {
		if !strings.Contains(text, want) {
			t.Errorf("缺少 %q:\n%s", want, text)
		}
	}
	st, _ := LoadSkillState(path)
	if !st.IsDisabled("a") || !st.IsDisabled("b") {
		t.Errorf("状态 = %+v", st)
	}
}

func TestSetSkillDisabledRejectsBadFile(t *testing.T) {
	path := writeMCP(t, `{"disabled": `)
	if err := SetSkillDisabled(path, "a", true); err == nil {
		t.Fatal("坏配置应报错，而不是覆盖掉")
	}
	b, _ := os.ReadFile(path)
	if string(b) != `{"disabled": ` {
		t.Errorf("不该改动坏文件:\n%s", b)
	}
}
