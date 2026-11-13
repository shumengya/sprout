package main

import (
	"os"
	"path/filepath"

	"github.com/shumengya/sprout/internal/agent/skills"
	"github.com/shumengya/sprout/internal/tui"
)

// SkillRoots 返回要扫描的技能目录，按优先级从高到低排列：靠前的先被采用，
// 同名技能后出现的被遮蔽（内容完全相同则静默跳过）。
//
// 顺序遵循 Agent Skills 的客户端约定：项目级覆盖用户级，客户端原生目录与
// 跨客户端的 .agents/skills 都扫。sprout 的“用户级”是数据目录（安装级），
// 不写用户 home。
func skillRoots(dataDir, cwd, home string) []skills.Root {
	var roots []skills.Root
	add := func(dir string, src skills.Source) {
		if dir != "" {
			roots = append(roots, skills.Root{Dir: dir, Source: src})
		}
	}
	if filepath.IsAbs(cwd) {
		add(filepath.Join(cwd, ".sprout", "skills"), skills.SourceProject)
		add(filepath.Join(cwd, ".agents", "skills"), skills.SourceProject)
	}
	add(filepath.Join(dataDir, "skills"), skills.SourceUser)
	if home != "" {
		add(filepath.Join(home, ".agents", "skills"), skills.SourceUser)
	}
	return roots
}

// skillInfos 把发现的技能转成界面用的形状：名字 + SKILL.md 路径。
// 带上路径是为了让 TUI 认出“模型正在读某个技能的正文”，在对话区标出来。
func skillInfos(list []skills.Skill) []tui.SkillInfo {
	if len(list) == 0 {
		return nil
	}
	out := make([]tui.SkillInfo, len(list))
	for i, sk := range list {
		out[i] = tui.SkillInfo{Name: sk.Name, Path: sk.Path}
	}
	return out
}

// loadSkills 扫描技能目录。技能是可选能力：扫不到就没装，读不了只记一条提示。
func loadSkills(dataDir, cwd, home string) (list []skills.Skill, problems []string) {
	// 数据目录下的技能目录预先建好，用户知道该往哪放；建不了不影响启动。
	dir := filepath.Join(dataDir, "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		problems = append(problems, "无法创建技能目录 "+dir+"："+err.Error())
	}
	return skills.Discover(skillRoots(dataDir, cwd, home))
}
