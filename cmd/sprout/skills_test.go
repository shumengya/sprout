package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
	if len(a.info.Skills) != 1 || a.info.Skills[0].Name != "demo-skill" {
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
	_, list, problems, err := skillCatalog(dir, "", "")
	if err != nil || len(problems) != 0 || len(list) != 1 {
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

// 面板停用技能后：写回 skills.jsonc，重建 agent 时不再把它的元数据发给模型。
func TestSkillStoreToggleRebuildsPrompt(t *testing.T) {
	srv := newModelServer(t, "bash")
	dir := t.TempDir()
	writeServerConfig(t, dir, srv.URL)
	skillDir := filepath.Join(dir, "skills", "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: demo\ndescription: SKILL_SECRET_DESC 用于测试。\n---\n正文\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a, err := buildApp(dir, testEnv(nil, shellSet(true, true)), nil)
	if err != nil || a.ag == nil {
		t.Fatalf("装配失败: %v / %s", err, a.disabled)
	}
	defer a.close()

	store := skillStore{a: a}
	if list := store.Skills(); len(list) != 1 || !list[0].Enabled {
		t.Fatalf("初始技能 = %+v", list)
	}
	notice, err := store.SetSkill("demo", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notice, "已停用技能 demo") {
		t.Errorf("提示 = %q", notice)
	}
	b, _ := os.ReadFile(config.SkillsPath(dir))
	if !strings.Contains(string(b), `"demo"`) {
		t.Errorf("应写回 skills.jsonc:\n%s", b)
	}

	before := a.ag
	a.applyMCP() // TUI 会在空闲时做这一步
	if a.ag == before {
		t.Fatal("改技能开关后应重建 agent")
	}
	if list := store.Skills(); len(list) != 1 || list[0].Enabled {
		t.Errorf("停用后的技能列表 = %+v", list)
	}
	if n := len(a.activeSkills()); n != 0 {
		t.Errorf("停用后不该有启用技能，得到 %d", n)
	}

	// 用重建后的 agent 发一次请求：技能目录里不该再有 demo。
	if err := a.ag.Run(t.Context(), agent.Input{Text: "你好"}, event.SinkFunc(func(event.Event) {})); err != nil {
		t.Fatal(err)
	}
	req := srv.bodies[len(srv.bodies)-1]
	if strings.Contains(req, "SKILL_SECRET_DESC") || strings.Contains(req, "## 可用技能") {
		t.Errorf("停用的技能不该出现在系统提示词里:\n%s", req)
	}

	// 再启用：配置里移走，agent 又能看到它。
	if _, err := store.SetSkill("demo", true); err != nil {
		t.Fatal(err)
	}
	a.applyMCP()
	if n := len(a.activeSkills()); n != 1 {
		t.Errorf("重新启用后应有 1 个技能，得到 %d", n)
	}
}

// 重新扫描会发现新放进目录的技能并自动注册，已有的开关状态不受影响。
func TestSkillRescanPicksUpNewSkill(t *testing.T) {
	dir := t.TempDir()
	a := &app{dataDir: dir, cwd: "", env: testEnv(nil, shellSet(true, true))}
	writeSkill := func(name string) {
		d := filepath.Join(dir, "skills", name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: " + name + "\ndescription: 演示。\n---\n正文\n"
		if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill("keep-off")
	if _, err := (skillStore{a: a}).Rescan(); err != nil {
		t.Fatal(err)
	}
	if _, err := (skillStore{a: a}).SetSkill("keep-off", false); err != nil {
		t.Fatal(err)
	}

	writeSkill("fresh")
	notice, err := (skillStore{a: a}).Rescan()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notice, "共 2 个，启用 1 个") {
		t.Errorf("提示 = %q", notice)
	}
	byName := map[string]bool{}
	for _, sk := range (skillStore{a: a}).Skills() {
		byName[sk.Name] = sk.Enabled
	}
	if byName["keep-off"] || !byName["fresh"] {
		t.Errorf("开关状态 = %v", byName)
	}
	if !a.skillsDirty {
		t.Error("重新扫描后应标记待重建")
	}
	// 自动注册写进了注册表，不是每次重扫都当作新技能。
	cfg, err := config.LoadSkills(config.SkillsPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Skills) != 2 {
		t.Errorf("注册表 = %+v", cfg.Skills)
	}
}

// 注册表写坏了在启动时就报错：静默当成空表会让所有技能一起消失。
func TestBrokenSkillsConfigFailsLoudly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(config.SkillsPath(dir), []byte(`{"skills": `), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := skillCatalog(dir, "", ""); err == nil {
		t.Fatal("坏的 skills.jsonc 应该报错")
	}
}

// .agents/skills 下的技能受 scanAgents 控制：关掉后不加载，打开后回来。
func TestScanAgentsToggleControlsAgentsSkills(t *testing.T) {
	root := t.TempDir()
	dataDir, cwd := filepath.Join(root, "data"), filepath.Join(root, "proj")
	dir := filepath.Join(cwd, ".agents", "skills", "from-agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: from-agents\ndescription: 演示。\n---\n正文\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, list, problems, err := skillCatalog(dataDir, cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || len(problems) != 0 {
		t.Fatalf("默认应加载 .agents 下的技能: %+v / %v", list, problems)
	}
	if list[0].Source != skills.SourceProject {
		t.Errorf("来源 = %q", list[0].Source)
	}

	a := &app{dataDir: dataDir, cwd: cwd, env: testEnv(nil, shellSet(true, true))}
	a.commitSkills(cfg, list)
	notice, err := (skillStore{a: a}).SetScanAgents(false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notice, "已关闭") {
		t.Errorf("提示 = %q", notice)
	}
	if n := len((skillStore{a: a}).Skills()); n != 0 {
		t.Errorf("关掉后不该再有技能，得到 %d", n)
	}
	if n := len(a.activeSkills()); n != 0 {
		t.Errorf("关掉后不该有启用技能，得到 %d", n)
	}
	// 注册条目保留着，重新打开就恢复。
	if cfg, _ := config.LoadSkills(config.SkillsPath(dataDir)); len(cfg.Skills) != 1 {
		t.Errorf("注册表不该被清掉: %+v", cfg.Skills)
	}

	if _, err := (skillStore{a: a}).SetScanAgents(true); err != nil {
		t.Fatal(err)
	}
	if n := len(a.activeSkills()); n != 1 {
		t.Errorf("重新打开后应有 1 个技能，得到 %d", n)
	}
}

// 手动注册任意位置的技能：路径不必在扫描目录里。
func TestManualRegistrationLoadsArbitraryPath(t *testing.T) {
	root := t.TempDir()
	dataDir, elsewhere := filepath.Join(root, "data"), filepath.Join(root, "somewhere", "my-skill")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(elsewhere, "SKILL.md"),
		[]byte("---\nname: my-skill\ndescription: 手动注册的技能。\n---\n正文\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"skills": {"my-skill": {"path": ` + strconv.Quote(filepath.ToSlash(elsewhere)) + `, "enable": true}}}`
	if err := os.WriteFile(config.SkillsPath(dataDir), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	_, list, problems, err := skillCatalog(dataDir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 || len(list) != 1 {
		t.Fatalf("技能 = %+v / %v", list, problems)
	}
	if list[0].Name != "my-skill" || list[0].Source != skills.SourceExtra {
		t.Errorf("技能 = %+v", list[0])
	}
}

// 注册表的键就是交给模型的名字：和 SKILL.md 里的 name 不一致时以注册名为准。
func TestRegistrationKeyWinsOverFrontmatterName(t *testing.T) {
	root := t.TempDir()
	dataDir, dir := filepath.Join(root, "data"), filepath.Join(root, "s", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: demo\ndescription: 演示。\n---\n正文\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"skills": {"renamed": {"path": ` + strconv.Quote(filepath.ToSlash(dir)) + `, "enable": true}}}`
	if err := os.WriteFile(config.SkillsPath(dataDir), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	_, list, problems, err := skillCatalog(dataDir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "renamed" {
		t.Fatalf("技能 = %+v（%v）", list, problems)
	}
	if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, "不一致") }) {
		t.Errorf("应提示名字不一致: %v", problems)
	}
}

// 旧版的 disabled 列表迁移成注册表，禁用状态保留。
func TestLegacyDisabledListMigrates(t *testing.T) {
	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, "skills", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: demo\ndescription: 演示。\n---\n正文\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.SkillsPath(dataDir), []byte(`{"disabled": ["demo"]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, list, _, err := skillCatalog(dataDir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if e := cfg.Skills["demo"]; e.Enable || e.Path == "" {
		t.Errorf("迁移后应保留禁用状态: %+v", e)
	}
	if len(list) != 1 {
		t.Errorf("技能 = %+v", list)
	}
	b, _ := os.ReadFile(config.SkillsPath(dataDir))
	if strings.Contains(string(b), `"disabled"`) {
		t.Errorf("迁移后不该留旧字段:\n%s", b)
	}
}
