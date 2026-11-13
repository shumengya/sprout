package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/config"
	"github.com/shumengya/sprout/internal/agent/event"
	"github.com/shumengya/sprout/internal/agent/skills"
)

// 技能目录真的出现在发给模型的系统提示词里，带 location，让模型能自己 read。
func TestSkillCatalogReachesModelRequest(t *testing.T) {
	srv := newModelServer(t, "bash")
	dir := t.TempDir()
	writeServerConfig(t, dir, srv.URL)
	skillDir := filepath.Join(dir, "skills", "demo-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: demo-skill\ndescription: SKILL_SECRET_DESC 用于测试。\n---\n# 正文\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	a, err := buildApp(dir, testEnv(nil, shellSet(true, true)), nil)
	if err != nil || a.ag == nil {
		t.Fatalf("装配失败: %v / %s", err, a.disabled)
	}
	defer a.close()
	if a.info.Skills == nil || a.info.Skills[0] != "demo-skill" {
		t.Fatalf("技能应被列进 info: %v", a.info.Skills)
	}
	if err := a.ag.Run(t.Context(), agent.Input{Text: "你好"}, event.SinkFunc(func(event.Event) {})); err != nil {
		t.Fatal(err)
	}

	req := srv.bodies[0]
	quoted, _ := json.Marshal(filepath.Join(skillDir, "SKILL.md"))
	location := strings.Trim(string(quoted), `"`)
	for _, want := range []string{"## 可用技能", "demo-skill", "SKILL_SECRET_DESC", location, "先用 read 工具读取"} {
		if !strings.Contains(req, want) {
			t.Errorf("请求里缺少 %q", want)
		}
	}
	// 技能正文不该预加载：那正是渐进式披露要避免的。
	if strings.Contains(req, "# 正文") {
		t.Error("技能正文不该进入系统提示词，应按需 read")
	}
}

// 模型 tools=false 时既不该连 MCP，也不该下发工具。
func TestDisabledToolsSkipMCP(t *testing.T) {
	srv := newModelServer(t, "bash")
	dir := t.TempDir()
	writeServerConfig(t, dir, srv.URL)
	// 把默认模型改成 tools=false。
	cfg, err := os.ReadFile(config.ModelsPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	patched := strings.Replace(string(cfg), `"tools": true`, `"tools": false`, 1)
	if patched == string(cfg) {
		t.Fatal("模板里没有可替换的 tools: true")
	}
	if err := os.WriteFile(config.ModelsPath(dir), []byte(patched), 0o600); err != nil {
		t.Fatal(err)
	}

	a, err := buildApp(dir, testEnv(nil, shellSet(true, true)), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	if a.mcp != nil {
		t.Error("不支持工具的模型不该连接 MCP 服务端")
	}
	if len(a.info.MCP) != 0 {
		t.Errorf("info.MCP = %+v", a.info.MCP)
	}
}

// 没有权限问题也要覆盖：技能目录中的正文能被 read 读到（模型的实际用法）。
func TestSkillBodyIsReadableWithReadTool(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills", "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: demo\ndescription: 演示。\n---\n# 步骤\n1. 做点什么\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	list, problems := loadSkills(dir, "", "")
	if len(problems) != 0 || len(list) != 1 {
		t.Fatalf("技能 = %+v / %v", list, problems)
	}
	if !strings.HasSuffix(list[0].Path, filepath.Join("demo", skills.FileName)) {
		t.Errorf("Path = %q", list[0].Path)
	}
	data, err := os.ReadFile(list[0].Path)
	if err != nil || !strings.Contains(string(data), "# 步骤") {
		t.Errorf("正文应可直接读取: %v", err)
	}
}
