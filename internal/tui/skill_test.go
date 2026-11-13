package tui

import (
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent/event"
)

// 模型按 location 用 read 读技能正文时，工具卡片上要标出技能名。
func TestSkillBadgeOnReadTool(t *testing.T) {
	o := Options{Runner: scriptRunner(), Info: testInfo}
	o.Info.Skills = []SkillInfo{{Name: "lark", Path: `C:\Users\smy\.agents\skills\lark\SKILL.md`}}
	m := newSized(t, o, 100, 30)
	begin(m, "查看一下我的飞书文档")
	apply(m, requested("c1", "read", `{"path":"C:\\Users\\smy\\.agents\\skills\\lark\\SKILL.md"}`))

	s := plain(m)
	mustContain(t, s, "[技能]", "lark", "read")

	// 普通文件不该被标成技能。
	apply(m, requested("c2", "read", `{"path":"C:\\work\\notes.md"}`))
	for _, line := range strings.Split(plain(m), "\n") {
		if strings.Contains(line, "notes.md") && strings.Contains(line, "[技能]") {
			t.Fatalf("普通文件被误标为技能: %q", line)
		}
	}
}

// 用 shell 命令读技能正文（cat / Get-Content）同样要标出来，路径分隔符与大小写不影响识别。
func TestSkillBadgeOnShellCommand(t *testing.T) {
	o := Options{Runner: scriptRunner(), Info: testInfo}
	o.Info.Skills = []SkillInfo{{Name: "lark", Path: `C:\Users\smy\.agents\skills\lark\SKILL.md`}}
	m := newSized(t, o, 120, 30)
	begin(m, "q")
	apply(m, requested("c1", "powershell",
		`{"command":"Get-Content -Raw 'c:/users/smy/.agents/skills/lark/SKILL.md'"}`))
	mustContain(t, plain(m), "[技能]", "lark")
}

func TestSkillForMatching(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner(), Info: testInfo}, 80, 24)
	m.opts.Info.Skills = []SkillInfo{
		{Name: "lark", Path: `C:\Users\smy\.agents\skills\lark\SKILL.md`},
		{Name: "demo", Path: `D:\proj\.agents\skills\demo\SKILL.md`},
	}
	cases := map[string]string{
		`{"path":"C:\\Users\\smy\\.agents\\skills\\lark\\SKILL.md"}`:                                    "lark",
		`{"path":"c:/users/smy/.agents/SKILLS/LARK/skill.md"}`:                                          "lark",
		`{"command":"cat C:/Users/smy/.agents/skills/demo/../../D:/proj/.agents/skills/demo/SKILL.md"}`: "demo",
		`{"command":"cat C:\\Users\\smy\\.agents\\skills\\lark\\references\\api.md"}`:                   "lark",
		`{"path":"D:\\work\\readme.md"}`:                                                                "",
		`{"command":"ls"}`:                                                                              "",
		`not json`:                                                                                      "",
	}
	for args, want := range cases {
		if got := m.skillFor(args); got != want {
			t.Errorf("skillFor(%s) = %q，期望 %q", args, got, want)
		}
	}
}

// 技能被遮蔽/失效时 Info 里没有它，卡片就不该标；标记不能凭空出现。
func TestNoSkillBadgeWithoutSkills(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 30)
	begin(m, "q")
	apply(m, requested("c1", "read", `{"path":"C:\\Users\\smy\\.agents\\skills\\lark\\SKILL.md"}`))
	mustNotContain(t, plain(m), "[技能]")
}

// 工具跑完、卡片重绘后标记仍要保留。
func TestSkillBadgeKeptAfterFinish(t *testing.T) {
	o := Options{Runner: scriptRunner(), Info: testInfo}
	o.Info.Skills = []SkillInfo{{Name: "lark", Path: `C:\Users\smy\.agents\skills\lark\SKILL.md`}}
	m := newSized(t, o, 100, 30)
	begin(m, "q")
	apply(m, requested("c1", "read", `{"path":"C:\\Users\\smy\\.agents\\skills\\lark\\SKILL.md"}`))
	apply(m, event.ToolFinished{ID: "c1", Name: "read", Stdout: "x", ExitCode: 0})
	if !strings.Contains(plain(m), "[技能]") {
		t.Fatal("完成后标记仍应保留")
	}
}
