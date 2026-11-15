package agent

import (
	"strings"
	"testing"
)

func TestPruneKeepsSkillBodies(t *testing.T) {
	// 技能正文是模型执行任务的依据，裁成开头 200 字符会让它做偏，所以不参与裁剪。
	body := strings.Repeat("步骤", 1000)
	h := []Message{
		{Role: RoleUser, Content: []Block{TextBlock("用技能做事")}},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "s1", Name: "read", Arguments: `{"path":"C:\\skills\\demo\\SKILL.md"}`}}},
		{Role: RoleTool, ToolID: "s1", Content: []Block{TextBlock(body)}},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "o1", Name: "read", Arguments: `{"path":"C:\\work\\big.log"}`}}},
		{Role: RoleTool, ToolID: "o1", Content: []Block{TextBlock(body)}},
	}
	if n := PruneToolOutputs(h, 0, 0); n != 1 {
		t.Fatalf("只该裁剪普通文件读取，收到 %d 条", n)
	}
	if h[2].Text() != body {
		t.Error("技能正文不能被裁剪")
	}
	if !isPruned(h[4].Text()) {
		t.Error("普通工具输出应被裁剪")
	}
}

func TestPruneWithoutSkillReadsWorksAsBefore(t *testing.T) {
	body := strings.Repeat("x", 1000)
	h := []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "o1", Name: "bash", Arguments: `{"command":"ls"}`}}},
		{Role: RoleTool, ToolID: "o1", Content: []Block{TextBlock(body)}},
	}
	if n := PruneToolOutputs(h, 0, 0); n != 1 {
		t.Errorf("普通调用应照常裁剪，收到 %d", n)
	}
}
