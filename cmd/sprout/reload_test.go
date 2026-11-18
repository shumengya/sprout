package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/config"
)

// reloadApp 装配一个可对话的 app，返回它和 models.jsonc 的路径。
func reloadApp(t *testing.T) (*app, string) {
	t.Helper()
	dir := t.TempDir()
	a, err := buildApp(dir, testEnv(map[string]string{"DEEPSEEK_API_KEY": "k"}, shellSet(true, true)), nil)
	if err != nil || a.ag == nil {
		t.Fatalf("装配失败: %v", err)
	}
	t.Cleanup(a.close)
	return a, config.ModelsPath(dir)
}

// editModels 把 models.jsonc 里的 old 换成 new（只换第一处），并把修改时间往后拨，
// 保证文件状态一定和之前不同（同一时刻写两次时修改时间可能相同）。
func editModels(t *testing.T, path, old, new string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), old) {
		t.Fatalf("models.jsonc 里没有 %q", old)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(raw), old, new, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	later := st.ModTime().Add(time.Minute)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
}

func TestModelsChangedWaitsUntilTheFileSettles(t *testing.T) {
	a, path := reloadApp(t)
	if a.modelsChanged() {
		t.Fatal("没改过不该报告变化")
	}
	editModels(t, path, `"contextWindow": 1000000`, `"contextWindow": 500000`)
	if a.modelsChanged() {
		t.Error("第一次看到新状态时可能还没写完，应等下一次确认")
	}
	if !a.modelsChanged() {
		t.Fatal("连续两次看到同一个新状态应报告变化")
	}
	a.reloadModels()
	if a.modelsChanged() || a.modelsChanged() {
		t.Error("读取之后不该再报告同一次改动")
	}
}

func TestReloadModelsAppliesNewSettingsOfTheCurrentModel(t *testing.T) {
	a, path := reloadApp(t)
	a.ag.Restore([]agent.Message{
		{Role: "user", Content: []agent.Block{agent.TextBlock("之前的问题")}},
		{Role: "assistant", Content: []agent.Block{agent.TextBlock("之前的回答")}, Reasoning: "同一个模型的思维链"},
	}, "旧摘要")
	before := a.ag

	editModels(t, path, `"contextWindow": 1000000`, `"contextWindow": 500000`) // 第一处是当前模型 deepseek/deepseek-flash
	info, notices := a.reloadModels()
	if a.ag == before || info.ContextWindow != 500000 || a.sel.Model.ContextWindow != 500000 {
		t.Fatalf("当前模型的新设置应生效并重建 agent: window=%d rebuilt=%v", info.ContextWindow, a.ag != before)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "新设置已生效") {
		t.Errorf("提示 = %q", notices)
	}
	h := a.ag.History()
	if len(h) != 2 || h[1].Text() != "之前的回答" || h[1].Reasoning != "同一个模型的思维链" || a.ag.Summary() != "旧摘要" {
		t.Errorf("同一个模型、同一个协议：历史、思维链和摘要都应原样保留: %+v %q", h, a.ag.Summary())
	}
}

func TestReloadModelsListsNewModelsWithoutRebuilding(t *testing.T) {
	a, path := reloadApp(t)
	before, n := a.ag, len(a.cfg.All())
	editModels(t, path, `"deepseek-anthropic": {`,
		`"extra": { "protocol": "openai-chat", "baseUrl": "https://example.com", "apiKey": "k", "models": { "m1": {} } },
    "deepseek-anthropic": {`)
	_, notices := a.reloadModels()
	if a.ag != before {
		t.Error("当前模型没变，不该重建 agent")
	}
	if len(a.cfg.All()) != n+1 || len(modelStore{a}.List()) != n+1 {
		t.Errorf("/model 应列出新加的模型: %d → %d", n, len(a.cfg.All()))
	}
	if len(notices) != 1 || strings.Contains(notices[0], "新设置") {
		t.Errorf("提示 = %q", notices)
	}
	if _, err := (modelStore{a}).Switch("extra/m1"); err != nil {
		t.Errorf("新加的模型应能直接切换: %v", err)
	}
}

func TestReloadModelsKeepsTheOldConfigOnErrors(t *testing.T) {
	a, path := reloadApp(t)
	before, cfg := a.ag, a.cfg
	editModels(t, path, `"default": "deepseek/deepseek-flash",`, `"default": "deepseek/deepseek-flash"`) // 少个逗号
	_, notices := a.reloadModels()
	if a.ag != before || a.cfg != cfg {
		t.Error("配置写错时应保持原样")
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "改动有误") || !strings.Contains(notices[0], "models.jsonc") {
		t.Errorf("应说明配置有误并定位: %q", notices)
	}
	if a.modelsChanged() || a.modelsChanged() {
		t.Error("同一份写错的内容不该反复触发读取")
	}
}

func TestReloadModelsKeepsUsingARemovedCurrentModel(t *testing.T) {
	a, path := reloadApp(t)
	before := a.ag
	editModels(t, path, `"default": "deepseek/deepseek-flash",`, `"default": "deepseek/renamed",`)
	editModels(t, path, `"deepseek-flash": {`, `"renamed": {`) // 第一处在 deepseek 下
	_, notices := a.reloadModels()
	if a.ag != before || a.sel.Ref() != "deepseek/deepseek-flash" {
		t.Errorf("当前模型被删时应继续用原来的设置: %s", a.sel.Ref())
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "已不在配置里") || !strings.Contains(notices[0], "/model") {
		t.Errorf("提示 = %q", notices)
	}
}

func TestReloadModelsWithoutKeyKeepsTheOldAgent(t *testing.T) {
	a, path := reloadApp(t)
	before := a.ag
	// 当前 provider 改用另一个没设置的环境变量：新设置拿不到 Key，旧的 agent 照用。
	editModels(t, path, `"apiKeyEnv": "DEEPSEEK_API_KEY"`, `"apiKeyEnv": "NOT_SET_KEY"`)
	_, notices := a.reloadModels()
	if a.ag != before {
		t.Error("拿不到 Key 时不该换掉能用的 agent")
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "没有生效") {
		t.Errorf("提示 = %q", notices)
	}
}
