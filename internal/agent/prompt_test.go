package agent

import (
	"os"
	"path/filepath"
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

// 支持识图时要明说图片已经能看到；否则模型会把带路径的提问当成文件任务，去 Test-Path 或找 OCR。
func TestPromptMentionsAttachedImagesWhenVision(t *testing.T) {
	p := BuildSystemPrompt(PromptInfo{
		OS: "Windows", Now: time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC),
		Tools: []string{"powershell"}, Vision: true,
	})
	for _, want := range []string{"已经能看到", "不要用工具去读取", "只是它的来源说明"} {
		if !strings.Contains(p, want) {
			t.Errorf("支持识图的提示词缺少 %q:\n%s", want, p)
		}
	}
	if strings.Contains(promptFor("powershell"), "已经能看到") {
		t.Error("不支持识图时不应出现图片说明")
	}
}

func TestPromptTemplateVariablesAndConditions(t *testing.T) {
	p, err := ParsePromptTemplate(`{{.OS}}|{{.Cwd}}|{{.Date}}|{{.Now.Format "15:04"}}|{{join .Tools ","}}|` +
		`{{if .Has "bash"}}B{{end}}{{if .Vision}}V{{end}}{{if not .Tools}}none{{end}}`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Render(PromptInfo{OS: "Linux", Cwd: "/w", Now: time.Date(2026, 1, 2, 9, 30, 0, 0, time.UTC),
		Tools: []string{"bash", "read"}, Vision: true})
	if err != nil {
		t.Fatal(err)
	}
	if want := "Linux|/w|2026-01-02|09:30|bash,read|BV"; got != want {
		t.Errorf("渲染结果 = %q，期望 %q", got, want)
	}
	got, _ = p.Render(PromptInfo{})
	if !strings.HasSuffix(got, "none") {
		t.Errorf("没有工具时应走 not .Tools 分支: %q", got)
	}
}

func TestPromptTemplateErrorsAreReportedAtParse(t *testing.T) {
	for name, text := range map[string]string{
		"语法错误":  `{{if .Vision}}没有 end`,
		"变量名写错": `{{.Workdir}}`,
		"函数不存在": `{{upper .OS}}`,
		"方法参数错": `{{if .Has}}x{{end}}`,
	} {
		if _, err := ParsePromptTemplate(text); err == nil {
			t.Errorf("%s 应在解析时报错", name)
		}
	}
}

func TestPromptTemplateCommentsAreNotSent(t *testing.T) {
	p, err := ParsePromptTemplate("{{- /* 给人看的说明 */ -}}\n正文")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := p.Render(PromptInfo{}); got != "正文" {
		t.Errorf("注释不应出现在提示词里: %q", got)
	}
}

// 仓库根的示例文件与内置模板逐字一致，避免两处各改各的。
func TestDefaultPromptMatchesExampleFile(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "SYSTEMPROMPT.example.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.ReplaceAll(string(b), "\r\n", "\n") != DefaultPromptTemplate {
		t.Fatal("SYSTEMPROMPT.example.md 与 agent.DefaultPromptTemplate 不一致，请同步修改")
	}
}

// 模板里的条件块很容易多出或吃掉空行；每种工具 × 识图组合都要保持「段落之间正好一个空行」。
func TestDefaultPromptLayoutInEveryEnvironment(t *testing.T) {
	sets := [][]string{nil, {"read"}, {"write"}, {"bash"}, {"powershell"}, {"read", "write"}, {"bash", "powershell", "read", "write", "ls"}}
	for _, tools := range sets {
		for _, vision := range []bool{false, true} {
			p := BuildSystemPrompt(PromptInfo{OS: "Windows", Cwd: ".", Tools: tools, Vision: vision})
			if strings.HasPrefix(p, "\n") || strings.Contains(p, "\n\n\n") {
				t.Errorf("tools=%v vision=%v 有多余空行:\n%s", tools, vision, p)
			}
			for _, line := range strings.Split(p, "\n")[1:] {
				if strings.HasPrefix(line, "## ") && !strings.Contains(p, "\n\n"+line) {
					t.Errorf("tools=%v vision=%v 标题 %q 前缺少空行", tools, vision, line)
				}
			}
		}
	}
}

// 曾经发生过：模型按进程名结束 sprout.exe，把正在运行的 Sprout 自己杀掉了。
func TestPromptForbidsKillingByProcessName(t *testing.T) {
	for _, tools := range [][]string{{"bash"}, {"powershell"}} {
		if p := promptFor(tools...); !strings.Contains(p, "不要按进程名批量结束") || !strings.Contains(p, "运行你的这个 Sprout") {
			t.Errorf("%v 可用时应禁止按进程名结束进程:\n%s", tools, p)
		}
	}
	if strings.Contains(promptFor("read"), "按进程名") {
		t.Error("没有 shell 时不应出现进程相关规则")
	}
}
