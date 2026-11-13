package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkill 在 root/name 下写一个 SKILL.md。
func writeSkill(t *testing.T, root, name, content string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDiscoverFindsSkillsAndIgnoresLooseFiles(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "pdf-processing", "---\nname: pdf-processing\ndescription: Handle PDFs.\n---\nbody\n")
	writeSkill(t, root, "code-review", "---\nname: code-review\ndescription: Review code.\n---\nbody\n")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("not a skill"), 0o644); err != nil {
		t.Fatal(err)
	}

	list, problems := Discover([]Root{{Dir: root, Source: SourceUser}})
	if len(problems) != 0 {
		t.Fatalf("不应有问题: %v", problems)
	}
	if len(list) != 2 || list[0].Name != "code-review" || list[1].Name != "pdf-processing" {
		t.Fatalf("技能 = %+v", list)
	}
	if list[0].Source != SourceUser {
		t.Errorf("来源 = %q", list[0].Source)
	}
	if !strings.HasSuffix(list[0].Path, FileName) {
		t.Errorf("Path 应指向 SKILL.md: %q", list[0].Path)
	}
	if filepath.Base(list[0].Dir) != "code-review" {
		t.Errorf("Dir = %q", list[0].Dir)
	}
}

func TestDiscoverNestedAndGitIgnored(t *testing.T) {
	root := t.TempDir()
	// 深层目录里的技能要被找到（monorepo 场景）。
	writeSkill(t, filepath.Join(root, "packages", "web"), "frontend", "---\ndescription: FE.\n---\n")
	// .git / node_modules 下的不扫。
	writeSkill(t, filepath.Join(root, ".git"), "hidden", "---\ndescription: no.\n---\n")
	writeSkill(t, filepath.Join(root, "node_modules"), "dep", "---\ndescription: no.\n---\n")
	// 技能目录里的子目录不再当技能扫。
	writeSkill(t, filepath.Join(root, "outer"), "inner", "---\ndescription: no.\n---\n")
	writeSkill(t, root, "outer", "---\ndescription: outer skill.\n---\n")

	list, _ := Discover([]Root{{Dir: root, Source: SourceProject}})
	var names []string
	for _, s := range list {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "frontend,outer" {
		t.Fatalf("技能 = %v", names)
	}
}

func TestDiscoverPrecedenceAndShadowWarning(t *testing.T) {
	project, user := t.TempDir(), t.TempDir()
	writeSkill(t, project, "dup", "---\nname: dup\ndescription: 项目版\n---\n")
	writeSkill(t, user, "dup", "---\nname: dup\ndescription: 用户版\n---\n")

	list, problems := Discover([]Root{{Dir: project, Source: SourceProject}, {Dir: user, Source: SourceUser}})
	if len(list) != 1 || list[0].Description != "项目版" {
		t.Fatalf("项目级应覆盖用户级: %+v", list)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "遮蔽") {
		t.Fatalf("应提示遮蔽: %v", problems)
	}
}

func TestLoadRejectsMissingDescriptionAndBrokenFrontmatter(t *testing.T) {
	root := t.TempDir()
	noDesc := writeSkill(t, root, "no-desc", "---\nname: no-desc\n---\nbody\n")
	if _, _, err := Load(noDesc, SourceUser); err == nil || !strings.Contains(err.Error(), "description") {
		t.Errorf("没有 description 应报错，得到 %v", err)
	}
	noFM := writeSkill(t, root, "no-fm", "# 直接是正文\n")
	if _, _, err := Load(noFM, SourceUser); err == nil || !strings.Contains(err.Error(), "frontmatter") {
		t.Errorf("没有 frontmatter 应报错，得到 %v", err)
	}

	// 坏技能不影响其他技能。
	writeSkill(t, root, "ok", "---\nname: ok\ndescription: fine.\n---\n")
	list, problems := Discover([]Root{{Dir: root, Source: SourceUser}})
	if len(list) != 1 || list[0].Name != "ok" {
		t.Fatalf("技能 = %+v", list)
	}
	if len(problems) != 2 {
		t.Fatalf("应有两处问题: %v", problems)
	}
}

func TestLoadLenientFrontmatter(t *testing.T) {
	root := t.TempDir()
	// 未加引号的冒号让严格 YAML 解析失败，但 description 仍能取到。
	dir := writeSkill(t, root, "loose", "---\nname: loose\ndescription: Use this skill when: the user asks about PDFs\n---\nbody\n")
	sk, warns, err := Load(dir, SourceUser)
	if err != nil {
		t.Fatalf("宽松模式应能加载: %v", err)
	}
	if sk.Description != "Use this skill when: the user asks about PDFs" {
		t.Errorf("description = %q", sk.Description)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "宽松") {
		t.Errorf("应提示宽松解析: %v", warns)
	}
}

func TestLoadBlockScalarDescription(t *testing.T) {
	root := t.TempDir()
	dir := writeSkill(t, root, "block", "---\nname: block\ndescription: >\n  第一行\n  第二行\nlicense: Apache-2.0\n---\n")
	sk, _, err := Load(dir, SourceUser)
	if err != nil {
		t.Fatal(err)
	}
	if sk.Description != "第一行 第二行" {
		t.Errorf("块标量应折成一行，得到 %q", sk.Description)
	}
	if sk.License != "Apache-2.0" {
		t.Errorf("license = %q", sk.License)
	}
}

func TestLoadWarnsNameMismatchAndTruncatesDescription(t *testing.T) {
	root := t.TempDir()
	long := strings.Repeat("字", MaxDescription+10)
	dir := writeSkill(t, root, "dir-name", "---\nname: other-name\ndescription: "+long+"\n---\n")
	sk, warns, err := Load(dir, SourceUser)
	if err != nil {
		t.Fatal(err)
	}
	if len(sk.Description) != len(strings.Repeat("字", MaxDescription)) {
		t.Errorf("description 应截断到 %d 字，实际 %d", MaxDescription, len(sk.Description))
	}
	joined := strings.Join(warns, "\n")
	if !strings.Contains(joined, "不一致") || !strings.Contains(joined, "截断") {
		t.Errorf("应提示名字不一致与截断: %v", warns)
	}
}

func TestAppendCatalog(t *testing.T) {
	list := []Skill{
		{Name: "a", Description: "do <a>", Path: "/x/a/SKILL.md"},
		{Name: "b", Description: "do b", Path: "/x/b/SKILL.md"},
	}
	if got := AppendCatalog("base", nil, true); got != "base" {
		t.Errorf("没有技能时应原样返回: %q", got)
	}
	got := AppendCatalog("base\n", list, true)
	for _, want := range []string{"## 可用技能", "read", "<name>a</name>", "do &lt;a&gt;", "/x/b/SKILL.md", "</available_skills>"} {
		if !strings.Contains(got, want) {
			t.Errorf("缺少 %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "do <a>") {
		t.Error("description 里的尖括号应转义，避免破坏 XML 结构")
	}
	if noRead := AppendCatalog("base", list, false); !strings.Contains(noRead, "bash / powershell") {
		t.Errorf("没有 read 时应改用 shell:\n%s", noRead)
	}
}

func TestDiscoverMissingRootIsSilent(t *testing.T) {
	list, problems := Discover([]Root{{Dir: filepath.Join(t.TempDir(), "nope"), Source: SourceUser}})
	if len(list) != 0 || len(problems) != 0 {
		t.Fatalf("未安装技能的目录不该报错: %v %v", list, problems)
	}
}
