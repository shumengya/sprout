package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shumengya/sprout/internal/agent/config"
	"github.com/shumengya/sprout/internal/agent/platform"
)

// shellSet 构造一个固定的检测结果，让装配逻辑的测试不依赖本机装了什么。
// 路径是否真实存在无所谓：这里只验证注册了哪些工具，不会执行它们。
func shellSet(bashOK, psOK bool) func(string, string) platform.Shells {
	return func(string, string) platform.Shells {
		var s platform.Shells
		if bashOK {
			s.Bash = &platform.Shell{Kind: platform.KindBash, Path: "/fake/bash", Label: "Git Bash"}
		} else {
			s.Problems = append(s.Problems, platform.Problem{Kind: platform.KindBash, Reason: "未找到 Git Bash"})
		}
		if psOK {
			s.PowerShell = &platform.Shell{Kind: platform.KindPowerShell, Path: "/fake/pwsh", Label: "PowerShell 7+", Edition: "7+"}
		} else {
			s.Problems = append(s.Problems, platform.Problem{Kind: platform.KindPowerShell, Reason: "未找到 PowerShell"})
		}
		return s
	}
}

func testEnv(vars map[string]string, detect func(string, string) platform.Shells) env {
	return env{
		getenv:  func(k string) string { return vars[k] },
		cwd:     filepath.Join("work", "dir"),
		version: "test",
		now:     func() time.Time { return time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC) },
		detect:  detect,
	}
}

func toolNames(a *app) string {
	var n []string
	for _, t := range a.info.Tools {
		n = append(n, t.Name)
	}
	return strings.Join(n, ",")
}

func TestFirstRunCreatesTemplateAndDisablesWithGuidance(t *testing.T) {
	dir := t.TempDir()
	a, err := buildApp(dir, testEnv(nil, shellSet(true, true)), nil)
	if err != nil {
		t.Fatal(err)
	}
	path := config.ModelsPath(dir)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("首次运行应生成 models.jsonc: %v", err)
	}
	if a.ag != nil || a.rec != nil {
		t.Fatal("没有 API Key 时不应构造 agent")
	}
	for _, want := range []string{path, "DEEPSEEK_API_KEY", "重启"} {
		if !strings.Contains(a.disabled, want) {
			t.Errorf("提示缺少 %q: %s", want, a.disabled)
		}
	}
	// 即使不能对话，工具检测结果仍要展示给用户。顺序由 tools.jsonc 的已知工具列表决定，
	// 外部工具（模板里的 ls 示例）排在内置工具之后。
	if toolNames(a) != "bash,powershell,read,write,edit,delete,ls" {
		t.Errorf("工具 = %q", toolNames(a))
	}
}

func TestExistingConfigIsNeverOverwritten(t *testing.T) {
	dir := t.TempDir()
	path := config.ModelsPath(dir)
	custom := strings.Replace(config.Template, `"apiKey": ""`, `"apiKey": "sk-mine"`, 1)
	if err := os.WriteFile(path, []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := buildApp(dir, testEnv(nil, shellSet(true, true)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != custom {
		t.Fatal("已有配置被改动")
	}
	if a.ag == nil || a.disabled != "" {
		t.Fatalf("配置里写了 apiKey，应可对话: disabled=%q", a.disabled)
	}
}

func TestMissingKeyOnExistingConfigPointsToConfigAndRestart(t *testing.T) {
	dir := t.TempDir()
	if _, err := config.EnsureTemplate(config.ModelsPath(dir)); err != nil {
		t.Fatal(err)
	}
	a, err := buildApp(dir, testEnv(nil, shellSet(true, true)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.ag != nil {
		t.Fatal("应禁用对话")
	}
	for _, want := range []string{"未配置 API Key", "DEEPSEEK_API_KEY", config.ModelsPath(dir), "重启"} {
		if !strings.Contains(a.disabled, want) {
			t.Errorf("提示缺少 %q: %s", want, a.disabled)
		}
	}
}

func writeConfig(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(config.ModelsPath(dir), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestEnvKeyEnablesAgentWithToolsAndPrompt(t *testing.T) {
	dir := t.TempDir()
	a, err := buildApp(dir, testEnv(map[string]string{"DEEPSEEK_API_KEY": "sk-env"}, shellSet(true, true)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.ag == nil || a.rec == nil || a.disabled != "" {
		t.Fatalf("环境变量里有 Key 应可对话: disabled=%q", a.disabled)
	}
	if a.sel.Ref() != "deepseek/deepseek-flash" || toolNames(a) != "bash,powershell,read,write,edit,delete,ls" {
		t.Errorf("sel=%s tools=%s", a.sel.Ref(), toolNames(a))
	}
	// 首次运行但 Key 已就绪：不禁用，只在启动提示里说明生成的配置文件位置（状态栏与模型）。
	if len(a.notices) != 2 || !strings.Contains(strings.Join(a.notices, " "), config.ModelsPath(dir)) ||
		!strings.Contains(strings.Join(a.notices, " "), config.StatuslinePath(dir)) {
		t.Errorf("notices = %v", a.notices)
	}
	// 一条消息都没发时不应创建会话文件。
	if a.rec.Path() != "" {
		t.Error("会话文件应懒创建")
	}
}

func TestOnlyAvailableToolsAreRegistered(t *testing.T) {
	cases := []struct {
		name        string
		bash, ps    bool
		wantTools   string
		wantUnavail string
	}{
		{"两者都有", true, true, "bash,powershell,read,write,edit,delete,ls", ""},
		{"只有 powershell", false, true, "powershell,read,write,edit,delete",
			"bash 已在 tools.jsonc 启用，但本机不可用：未找到 Git Bash|ls 需要 bash，但本机不可用：未找到 Git Bash"},
		{"只有 bash", true, false, "bash,read,write,edit,delete,ls", "powershell 已在 tools.jsonc 启用，但本机不可用：未找到 PowerShell"},
		{"都没有", false, false, "read,write,edit,delete", "bash 已在 tools.jsonc 启用，但本机不可用：未找到 Git Bash|" +
			"powershell 已在 tools.jsonc 启用，但本机不可用：未找到 PowerShell|ls 需要 bash，但本机不可用：未找到 Git Bash"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, err := buildApp(t.TempDir(), testEnv(map[string]string{"DEEPSEEK_API_KEY": "k"}, shellSet(c.bash, c.ps)), nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := toolNames(a); got != c.wantTools {
				t.Errorf("工具 = %q，期望 %q", got, c.wantTools)
			}
			if strings.Join(a.info.Unavailable, "|") != c.wantUnavail {
				t.Errorf("Unavailable = %v，期望 %q", a.info.Unavailable, c.wantUnavail)
			}
			if a.ag == nil {
				t.Error("没有工具也应能对话，只是系统提示里不提工具")
			}
		})
	}
}

func TestShellPathsFromConfigReachDetector(t *testing.T) {
	dir := t.TempDir()
	cfg := strings.Replace(config.Template, `"bashPath": ""`, `"bashPath": "/my/bash"`, 1)
	writeConfig(t, dir, strings.Replace(cfg, `"powershellPath": ""`, `"powershellPath": "/my/pwsh"`, 1))
	var gotBash, gotPS string
	_, err := buildApp(dir, testEnv(nil, func(b, p string) platform.Shells { gotBash, gotPS = b, p; return platform.Shells{} }), nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotBash != "/my/bash" || gotPS != "/my/pwsh" {
		t.Fatalf("探测器收到 bash=%q ps=%q", gotBash, gotPS)
	}
}

func TestToolsFalseModelGetsNoTools(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, strings.Replace(config.Template, `"tools": true`, `"tools": false`, 1))
	a, err := buildApp(dir, testEnv(map[string]string{"DEEPSEEK_API_KEY": "k"}, shellSet(true, true)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if toolNames(a) != "" || a.ag == nil {
		t.Fatalf("tools=false 时不应注册工具: %q", toolNames(a))
	}
	if len(a.notices) == 0 || !strings.Contains(strings.Join(a.notices, "\n"), "tools=false") {
		t.Errorf("应告知用户原因: %v", a.notices)
	}
}

func TestBadConfigIsFatalWithLocation(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "{\n  \"default\": \"a/b\"\n  \"providers\": {}\n}\n")
	_, err := buildApp(dir, testEnv(nil, shellSet(true, true)), nil)
	var pe *config.ParseError
	if !errors.As(err, &pe) || pe.Line != 3 {
		t.Fatalf("语法错误应带行号: %v", err)
	}
	writeConfig(t, dir, `{"default":"x/y","providers":{"p":{"protocol":"anthropic","baseUrl":"https://x"}}}`)
	_, err = buildApp(dir, testEnv(nil, shellSet(true, true)), nil)
	var ve *config.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("内容错误应是 ValidationError: %v", err)
	}
}

func TestResponsesProtocolSelectable(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, strings.Replace(config.Template, `"default": "deepseek/deepseek-flash"`, `"default": "openai-responses/gpt-5"`, 1))
	a, err := buildApp(dir, testEnv(map[string]string{"OPENAI_API_KEY": "k"}, shellSet(false, true)), nil)
	if err != nil || a.ag == nil || a.sel.Provider.Protocol != config.ProtocolResponses {
		t.Fatalf("应能选用 Responses 协议: err=%v sel=%+v", err, a.sel)
	}
}

func TestAnthropicAndGoogleProtocolSelectable(t *testing.T) {
	cases := []struct {
		ref, envName, protocol string
	}{
		{"anthropic/claude-sonnet-5-5", "ANTHROPIC_API_KEY", config.ProtocolAnthropic},
		{"google/gemini-2.5-pro", "GEMINI_API_KEY", config.ProtocolGoogle},
	}
	for _, c := range cases {
		dir := t.TempDir()
		writeConfig(t, dir, strings.Replace(config.Template, `"default": "deepseek/deepseek-flash"`, `"default": "`+c.ref+`"`, 1))
		a, err := buildApp(dir, testEnv(map[string]string{c.envName: "k"}, shellSet(false, true)), nil)
		if err != nil || a.ag == nil || a.sel.Provider.Protocol != c.protocol {
			t.Fatalf("%s: err=%v sel=%+v", c.protocol, err, a.sel)
		}
	}
}

// tools.jsonc 里给内置工具写 description / parameters，会覆盖程序自带的版本；
// 覆盖只影响模型看到的元数据，工具的执行行为不变。
func TestBuiltinToolDescriptionAndParametersCanBeOverridden(t *testing.T) {
	dir := t.TempDir()
	tools := `{
  "tools": {
    "read": {
      "enable": true,
      "description": "只读 Markdown",
      "parameters": {
        "type": "object",
        "properties": { "path": { "type": "string" } },
        "required": ["path"]
      }
    },
    "write": { "enable": true }
  }
}`
	if err := os.WriteFile(config.ToolsPath(dir), []byte(tools), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := buildApp(dir, testEnv(map[string]string{"DEEPSEEK_API_KEY": "k"}, shellSet(false, false)), nil)
	if err != nil {
		t.Fatal(err)
	}
	read, ok := a.reg.Get("read")
	if !ok {
		t.Fatal("read 应已注册")
	}
	if got := read.Description(); got != "只读 Markdown" {
		t.Errorf("read 的描述 = %q，应使用配置里的覆盖值", got)
	}
	if !strings.Contains(string(read.Parameters()), "path") {
		t.Errorf("read 的参数 = %s，应使用配置里的 schema", read.Parameters())
	}
	if strings.Contains(string(read.Parameters()), "offset") {
		t.Error("覆盖后的参数 schema 不应再含内置的 offset")
	}

	// 没写覆盖的工具沿用内置描述。
	write, ok := a.reg.Get("write")
	if !ok {
		t.Fatal("write 应已注册")
	}
	if strings.Contains(write.Description(), "只读 Markdown") || write.Description() == "" {
		t.Errorf("write 应沿用内置描述，得到 %q", write.Description())
	}
}

// 内置工具写了不合法的 parameters 时，启动就报错，而不是把坏 schema 发给模型。
func TestBuiltinToolInvalidParametersRejected(t *testing.T) {
	dir := t.TempDir()
	tools := `{"tools": {"read": {"enable": true, "parameters": {"type": "string"}}}}`
	if err := os.WriteFile(config.ToolsPath(dir), []byte(tools), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := buildApp(dir, testEnv(map[string]string{"DEEPSEEK_API_KEY": "k"}, shellSet(false, false)), nil)
	if err == nil || !strings.Contains(err.Error(), "parameters.type") {
		t.Errorf("应拒绝不合法的 parameters，得到 %v", err)
	}
}

// 升级后才新增的内置工具，旧配置里没有条目时要说明“没有这个条目”，
// 和“写了但关着”区分开：前者告诉用户补哪一行。
func TestMissingBuiltinEntryIsReportedWithHowToAdd(t *testing.T) {
	dir := t.TempDir()
	tools := `{ "tools": { "read": { "enable": true }, "write": { "enable": true }, "edit": { "enable": false } } }`
	if err := os.WriteFile(config.ToolsPath(dir), []byte(tools), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := buildApp(dir, testEnv(map[string]string{"DEEPSEEK_API_KEY": "k"}, shellSet(false, false)), nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(a.info.Unavailable, "\n")
	for _, want := range []string{
		`bash 未启用（tools.jsonc 里没有这个条目；加上 "bash": { "enable": true } 即可）`,
		`powershell 未启用（tools.jsonc 里没有这个条目；加上 "powershell": { "enable": true } 即可）`,
		"edit 未启用（tools.jsonc 中设成了 enable: false）",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("缺少提示 %q，实际:\n%s", want, joined)
		}
	}
	if _, ok := a.reg.Get("edit"); ok {
		t.Error("edit 写了 enable: false，不应注册")
	}
}
