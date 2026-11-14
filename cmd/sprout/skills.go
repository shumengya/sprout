package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shumengya/sprout/internal/agent/config"
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

// skillInfos 把发现的技能转成界面用的形状。带上路径是为了让 TUI 认出
// “模型正在读某个技能的正文”，在对话区标出来；Enabled 来自 skills.jsonc 的开关。
func skillInfos(list []skills.Skill, state *config.SkillState) []tui.SkillInfo {
	if len(list) == 0 {
		return nil
	}
	out := make([]tui.SkillInfo, len(list))
	for i, sk := range list {
		out[i] = tui.SkillInfo{
			Name: sk.Name, Path: sk.Path, Desc: sk.Description,
			Source: string(sk.Source), Enabled: !state.IsDisabled(sk.Name),
		}
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

// loadSkillState 读取技能开关。文件坏了只是让所有技能回到启用状态，
// 连同一句提示交给用户；技能不该因为一个配置文件写错而整体消失。
func loadSkillState(dataDir string) (*config.SkillState, []string) {
	st, err := config.LoadSkillState(config.SkillsStatePath(dataDir))
	if err != nil {
		return &config.SkillState{}, []string{"读取技能开关失败，所有技能按启用处理：" + err.Error()}
	}
	return st, nil
}

// activeSkills 返回启用的技能；系统提示词里只列这些。
func (a *app) activeSkills() []skills.Skill {
	a.mu.Lock()
	list, state := a.skills, a.skillState
	a.mu.Unlock()
	out := make([]skills.Skill, 0, len(list))
	for _, sk := range list {
		if !state.IsDisabled(sk.Name) {
			out = append(out, sk)
		}
	}
	return out
}

// skillInfosLocked 按当前开关生成展示信息。
func (a *app) skillInfosLocked() []tui.SkillInfo {
	a.mu.Lock()
	list, state := a.skills, a.skillState
	a.mu.Unlock()
	return skillInfos(list, state)
}

// commitSkills 在锁内换上一批技能与开关状态，供面板操作使用。
func (a *app) commitSkills(list []skills.Skill, state *config.SkillState) {
	a.mu.Lock()
	a.skills, a.skillState, a.skillsDirty = list, state, true
	a.mu.Unlock()
}

// skillStore 把 app 适配成 tui.SkillPanel。
//
// 操作在 tea.Cmd 的 goroutine 里跑；完成后由 TUI 在空闲时调 applyMCP 重建 agent，
// 因为技能目录是系统提示词的一部分，改完必须重造 agent 才生效。
type skillStore struct{ a *app }

var _ tui.SkillPanel = skillStore{}

func (s skillStore) Skills() []tui.SkillInfo { return s.a.skillInfosLocked() }

// SetSkill 启用或停用一个技能：写回 skills.jsonc。技能文件本身不动。
func (s skillStore) SetSkill(name string, enable bool) (string, error) {
	a := s.a
	path := config.SkillsStatePath(a.dataDir)
	if err := config.SetSkillDisabled(path, name, !enable); err != nil {
		return "", err
	}
	state, problems := loadSkillState(a.dataDir)
	if len(problems) > 0 {
		return "", fmt.Errorf("%s", strings.Join(problems, "；"))
	}
	a.mu.Lock()
	list := a.skills
	a.skillState = state
	a.skillsDirty = true
	a.mu.Unlock()
	// 校验名字确实在列表里，否则提示词不会有任何变化，用户会以为开关坏了。
	found := false
	for _, sk := range list {
		if sk.Name == name {
			found = true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("没有找到技能 %q，按 r 重新扫描", name)
	}
	action := "启用"
	if !enable {
		action = "停用"
	}
	return fmt.Sprintf("已%s技能 %s，下一轮对话生效", action, name), nil
}

// Rescan 重新扫描技能目录，并重读开关文件。
//
// 重新扫描是有意的：用户可能刚把技能目录拷进来或删掉，只刷新列表不会发现它们。
func (s skillStore) Rescan() (string, error) {
	a := s.a
	list, problems := loadSkills(a.dataDir, a.cwd, a.env.home)
	state, stateProblems := loadSkillState(a.dataDir)
	a.commitSkills(list, state)

	enabled := 0
	for _, sk := range list {
		if !state.IsDisabled(sk.Name) {
			enabled++
		}
	}
	notice := fmt.Sprintf("已重新扫描技能：共 %d 个，启用 %d 个", len(list), enabled)
	for _, p := range append(problems, stateProblems...) {
		notice += "\n" + p
	}
	return notice, nil
}
