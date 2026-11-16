package main

import (
	"os"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent/config"
)

// 启动时优先用 settings.jsonc 里记的模型，而不是 models.jsonc 的 default。
func TestStartupRestoresLastModelAndEffort(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, effortConfig)
	// models.jsonc 的 default 是 ds/flash（默认 high），上次用的是 claude/opus + xhigh。
	if err := config.SaveSettings(config.SettingsPath(dir), config.Settings{Model: "claude/opus", Effort: "xhigh"}); err != nil {
		t.Fatal(err)
	}
	a, err := buildApp(dir, testEnv(nil, shellSet(true, true)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.sel.Ref() != "claude/opus" {
		t.Errorf("模型 = %s，期望沿用上次的 claude/opus", a.sel.Ref())
	}
	if a.effort != "xhigh" || a.info.Effort != "xhigh" {
		t.Errorf("档位 = %q info = %q", a.effort, a.info.Effort)
	}
	if th := a.ag.Thinking(); th.Effort != "xhigh" {
		t.Errorf("agent 思考参数 = %+v", th)
	}
}

// 记的模型已从 models.jsonc 删掉时退回 default，并提示一句，不挡住启动。
func TestStartupFallsBackWhenRememberedModelGone(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, effortConfig)
	if err := config.SaveSettings(config.SettingsPath(dir), config.Settings{Model: "gone/model"}); err != nil {
		t.Fatal(err)
	}
	a, err := buildApp(dir, testEnv(nil, shellSet(true, true)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.sel.Ref() != "ds/flash" {
		t.Errorf("模型 = %s，期望退回 default", a.sel.Ref())
	}
	if !strings.Contains(strings.Join(a.notices, "\n"), "gone/model") {
		t.Errorf("应提示上次的模型已不在配置里：%v", a.notices)
	}
}

// 记的档位在新模型上没有时退回它的默认档位；--effort 始终优先。
func TestStartupEffortPriority(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, effortConfig)
	// claude/opus 只声明了 low..max，记下的 off 不存在。
	if err := config.SaveSettings(config.SettingsPath(dir), config.Settings{Model: "claude/opus", Effort: "off"}); err != nil {
		t.Fatal(err)
	}
	a, err := buildApp(dir, testEnv(nil, shellSet(true, true)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.effort != config.EffortAuto {
		t.Errorf("档位不存在时应退回该模型的默认值 auto，得到 %q", a.effort)
	}

	e := testEnv(nil, shellSet(true, true))
	e.effort = "low"
	a, err = buildApp(dir, e, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.sel.Ref() != "claude/opus" || a.effort != "low" {
		t.Errorf("--effort 应覆盖记录：model=%s effort=%q", a.sel.Ref(), a.effort)
	}
}

// settings.jsonc 坏掉只是回到默认值，不影响启动。
func TestStartupIgnoresBrokenState(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, effortConfig)
	if err := os.WriteFile(config.SettingsPath(dir), []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := buildApp(dir, testEnv(nil, shellSet(true, true)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.sel.Ref() != "ds/flash" || a.effort != "high" {
		t.Errorf("应退回默认：model=%s effort=%q", a.sel.Ref(), a.effort)
	}
	if !strings.Contains(strings.Join(a.notices, "\n"), "settings.jsonc") {
		t.Errorf("应提示 settings.jsonc 解析失败：%v", a.notices)
	}
}

// /model 与 /effort 改动后立刻落盘，下次启动才有得恢复。
func TestSwitchAndEffortArePersisted(t *testing.T) {
	a, err := effortApp(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (modelStore{a}).Switch("claude/opus"); err != nil {
		t.Fatal(err)
	}
	if _, err := (effortStore{a}).Set("max"); err != nil {
		t.Fatal(err)
	}
	st, err := config.LoadSettings(config.SettingsPath(a.dataDir))
	if err != nil {
		t.Fatal(err)
	}
	if st.Model != "claude/opus" || st.Effort != "max" {
		t.Errorf("落盘内容 = %+v", st)
	}
	// 失败的操作不改记录。
	if _, err := (effortStore{a}).Set("xhigh"); err != nil {
		t.Fatal(err)
	}
	if _, err := (effortStore{a}).Set("ultra"); err == nil {
		t.Fatal("不存在的档位应报错")
	}
	if st, _ := config.LoadSettings(config.SettingsPath(a.dataDir)); st.Effort != "xhigh" {
		t.Errorf("失败的 Set 不应改记录：%+v", st)
	}
}

// run 模式不改记录：脚本里临时用 --effort 不该改掉用户交互时的选择。
// 启动会补一份 settings.jsonc 模板，但里面的 model 与 effort 必须是空的。
func TestBuildAppDoesNotPersistOnStartup(t *testing.T) {
	a, err := effortApp(t, "low")
	if err != nil {
		t.Fatal(err)
	}
	st, err := config.LoadSettings(config.SettingsPath(a.dataDir))
	if err != nil {
		t.Fatal(err)
	}
	if st.Model != "" || st.Effort != "" {
		t.Errorf("启动不该记下这次的选择：%+v", st)
	}
}
