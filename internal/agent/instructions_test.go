package agent

import (
	"strings"
	"testing"
)

func TestAppendInstructions(t *testing.T) {
	if got := AppendInstructions("基础", nil); got != "基础" {
		t.Fatalf("没有说明时不应改动: %q", got)
	}
	got := AppendInstructions("基础\n\n", []Instruction{
		{Path: "/data/AGENTS.md", Scope: "全局", Text: "  全局内容\n"},
		{Path: "/p/AGENTS.md", Scope: "项目", Text: "项目内容"},
	})
	want := []string{
		"基础\n\n## 用户与项目说明\n",
		"<instructions scope=\"全局\" path=\"/data/AGENTS.md\">\n全局内容\n</instructions>",
		"<instructions scope=\"项目\" path=\"/p/AGENTS.md\">\n项目内容\n</instructions>",
	}
	last := -1
	for _, w := range want {
		i := strings.Index(got, w)
		if i < 0 || i < last {
			t.Fatalf("缺少或顺序不对 %q:\n%s", w, got)
		}
		last = i
	}
}
