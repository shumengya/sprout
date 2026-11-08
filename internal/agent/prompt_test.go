package agent

import (
	"strings"
	"testing"
	"time"
)

func promptFor(tools ...string) string {
	return BuildSystemPrompt(PromptInfo{
		OS:    "Windows",
		Cwd:   `D:\SmyProjects\sprout`,
		Now:   time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC),
		Tools: tools,
	})
}

func TestPromptIncludesEnvironment(t *testing.T) {
	p := promptFor("powershell")
	for _, want := range []string{"Windows", `D:\SmyProjects\sprout`, "2026-07-27", "Sprout"} {
		if !strings.Contains(p, want) {
			t.Errorf("提示词缺少 %q", want)
		}
	}
}

func TestPromptBothShellsGivesPreference(t *testing.T) {
	p := promptFor("powershell", "bash")
	if !strings.Contains(p, "powershell、bash") || !strings.Contains(p, "优先用 powershell") ||
		!strings.Contains(p, "POSIX") {
		t.Errorf("两个 shell 都可用时应说明选择规则:\n%s", p)
	}
}

func TestPromptSingleShellHasNoPreferenceLine(t *testing.T) {
	for _, tools := range [][]string{{"bash"}, {"powershell"}} {
		p := promptFor(tools...)
		if strings.Contains(p, "两者都可用") {
			t.Errorf("只有 %v 时不应出现选择规则", tools)
		}
		if !strings.Contains(p, tools[0]) || !strings.Contains(p, "无状态") {
			t.Errorf("应列出工具并说明无状态: %s", p)
		}
	}
	// 只列出实际可用的工具。
	if strings.Contains(promptFor("bash"), "powershell") {
		t.Error("没有 powershell 时提示词里不应出现它")
	}
}

func TestPromptWithoutToolsAdmitsLimits(t *testing.T) {
	p := promptFor()
	if !strings.Contains(p, "没有可用的终端工具") || strings.Contains(p, "无状态") {
		t.Errorf("没有工具时应如实说明:\n%s", p)
	}
}
