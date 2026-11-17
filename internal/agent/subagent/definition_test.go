package subagent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeDef(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func byName(list []Definition) map[string]Definition {
	m := map[string]Definition{}
	for _, d := range list {
		m[d.Name] = d
	}
	return m
}

func TestLoadBuiltinsOnlyWhenNoDirs(t *testing.T) {
	list, problems := Load([]Root{{Dir: filepath.Join(t.TempDir(), "missing"), Source: SourceUser}})
	if len(problems) != 0 {
		t.Fatalf("目录不存在不该算问题: %v", problems)
	}
	m := byName(list)
	if _, ok := m[DefaultAgent]; !ok {
		t.Fatalf("缺少内置 %s: %v", DefaultAgent, list)
	}
	if ex := m["explore"]; ex.Tools == nil || ex.Allows("write") || !ex.Allows("read") {
		t.Errorf("explore 应只允许只读工具: %+v", ex.Tools)
	}
	if !m[DefaultAgent].Allows("anything") {
		t.Error("general 不限工具")
	}
}

func TestLoadParsesFrontmatterAndBody(t *testing.T) {
	dir := t.TempDir()
	writeDef(t, dir, "reviewer.md", "\xef\xbb\xbf---\r\nname: reviewer\r\ndescription: |\r\n  审查改动\r\n  找出 bug\r\ntools: read, bash, mcp__gh__*\r\nmodel: inherit\r\neffort: HIGH\r\n---\r\n\r\n只看 diff，不改文件。\r\n")
	writeDef(t, dir, "list.md", "---\ndescription: 列表写法\ntools:\n  - read\n  - powershell\nmodel: deepseek/deepseek-pro\n---\n")
	writeDef(t, dir, "README.md", "随便写点说明")
	writeDef(t, dir, "notes.txt", "不是定义")

	list, problems := Load([]Root{{Dir: dir, Source: SourceProject}})
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	m := byName(list)
	r := m["reviewer"]
	if r.Description != "审查改动 找出 bug" || r.Prompt != "只看 diff，不改文件。" || r.Model != "" || r.Effort != "high" || r.Source != SourceProject {
		t.Errorf("reviewer = %+v", r)
	}
	if !slices.Equal(r.Tools, []string{"read", "bash", "mcp__gh__*"}) || !r.Allows("mcp__gh__issue") || r.Allows("mcp__other__x") {
		t.Errorf("reviewer tools = %v", r.Tools)
	}
	l := m["list"] // 没写 name 时用文件名
	if !slices.Equal(l.Tools, []string{"read", "powershell"}) || l.Model != "deepseek/deepseek-pro" || l.Prompt != "" {
		t.Errorf("list = %+v", l)
	}
	if _, ok := m["README"]; ok {
		t.Error("README.md 不是定义")
	}
}

func TestLoadLaterRootsOverride(t *testing.T) {
	user, proj := t.TempDir(), t.TempDir()
	writeDef(t, user, "explore.md", "---\ndescription: 用户版 explore\nmodel: a/cheap\n---\n")
	writeDef(t, user, "docs.md", "---\ndescription: 用户版 docs\n---\n")
	writeDef(t, proj, "docs.md", "---\ndescription: 项目版 docs\n---\n")
	m := byName(must(Load([]Root{{user, SourceUser}, {proj, SourceProject}})))
	if m["explore"].Description != "用户版 explore" || m["explore"].Tools != nil {
		t.Errorf("用户定义应整个替换内置 explore: %+v", m["explore"])
	}
	if m["docs"].Description != "项目版 docs" {
		t.Errorf("项目级应覆盖用户级: %+v", m["docs"])
	}
}

func must(list []Definition, problems []string) []Definition {
	if len(problems) != 0 {
		panic(strings.Join(problems, "; "))
	}
	return list
}

func TestLoadReportsBadFiles(t *testing.T) {
	dir := t.TempDir()
	writeDef(t, dir, "nofm.md", "没有 frontmatter")
	writeDef(t, dir, "nodesc.md", "---\nname: nodesc\n---\n")
	writeDef(t, dir, "Bad Name.md", "---\ndescription: x\n---\n")
	writeDef(t, dir, "badtools.md", "---\ndescription: x\ntools: 3\n---\n")
	writeDef(t, dir, "badglob.md", "---\ndescription: x\ntools: \"[\"\n---\n")
	writeDef(t, dir, "ok.md", "---\ndescription: 正常\ntools: []\n---\n")
	list, problems := Load([]Root{{dir, SourceUser}})
	if len(problems) != 5 {
		t.Fatalf("应有 5 条问题，得到 %d: %v", len(problems), problems)
	}
	for _, want := range []string{"frontmatter", "description 为空", "不合法", "列表", "通配"} {
		if !strings.Contains(strings.Join(problems, "\n"), want) {
			t.Errorf("问题里缺少 %q: %v", want, problems)
		}
	}
	ok := byName(list)["ok"]
	if ok.Tools == nil || len(ok.Tools) != 0 || ok.Allows("read") {
		t.Errorf("tools: [] 表示一个工具都不给: %+v", ok.Tools)
	}
}
