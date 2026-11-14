package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/shumengya/sprout/internal/agent/config"
	"github.com/shumengya/sprout/internal/agent/skills"
	"github.com/shumengya/sprout/internal/tui"
)

// agentSkillRoots 返回 .agents/skills 的位置。scanAgents 关闭时这些目录下的技能一律不加载，
// 所以需要单独算一份来判断某个已注册的技能是不是被开关挡住的。
func agentSkillRoots(cwd, home string) []string {
	var roots []string
	if filepath.IsAbs(cwd) {
		roots = append(roots, filepath.Join(cwd, ".agents", "skills"))
	}
	if home != "" {
		roots = append(roots, filepath.Join(home, ".agents", "skills"))
	}
	return roots
}

// underDir 报告 path 是否落在 root 目录之内（含 root 本身）。
func underDir(path, root string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// skillRoots 返回要扫描的技能目录，按优先级从高到低排列：靠前的先被采用，
// 同名技能后出现的被遮蔽（内容完全相同则静默跳过）。
//
// 扫到的新技能会被自动登记进 skills.jsonc（注册才生效）。.agents/skills 只在
// scanAgents 开启时扫：那是别的 agent 客户端的目录，用户可以整体关掉。
func skillRoots(dataDir, cwd, home string, scanAgents bool) []skills.Root {
	var roots []skills.Root
	add := func(dir string, src skills.Source) {
		if dir != "" {
			roots = append(roots, skills.Root{Dir: dir, Source: src})
		}
	}
	if filepath.IsAbs(cwd) {
		add(filepath.Join(cwd, ".sprout", "skills"), skills.SourceProject)
	}
	add(filepath.Join(dataDir, "skills"), skills.SourceUser)
	if scanAgents {
		if filepath.IsAbs(cwd) {
			add(filepath.Join(cwd, ".agents", "skills"), skills.SourceProject)
		}
		if home != "" {
			add(filepath.Join(home, ".agents", "skills"), skills.SourceUser)
		}
	}
	return roots
}

// sourceFor 判断技能目录属于哪一处扫描位置，都不匹配时算自定义（手动注册的路径）。
func sourceFor(dir, dataDir, cwd, home string, scanAgents bool) skills.Source {
	for _, r := range skillRoots(dataDir, cwd, home, scanAgents) {
		if underDir(dir, r.Dir) {
			return r.Source
		}
	}
	return skills.SourceExtra
}

// skillCatalog 扫描技能目录、把新发现的技能登记进 skills.jsonc，再按注册表加载。
//
// 只有注册表里的技能才生效：数据目录 skills/ 与 .agents/skills 里新出现的技能
// 在这里被自动补进去，用户手动注册的任意路径则原样加载。返回值里的 problems
// 是需要告知用户、但不影响启动的问题（遮蔽、解析失败、被开关挡住等）。
func skillCatalog(dataDir, cwd, home string) (cfg *config.SkillsFile, list []skills.Skill, problems []string, err error) {
	path := config.SkillsPath(dataDir)
	// 数据目录可能还不存在（首次运行、测试里的临时目录），先建出来再写模板。
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, nil, nil, err
	}
	if _, err := config.EnsureSkillsTemplate(path); err != nil {
		return nil, nil, nil, err
	}
	// 数据目录下的技能目录预先建好，用户知道该往哪放；建不了不影响启动。
	if err := os.MkdirAll(filepath.Join(dataDir, "skills"), 0o755); err != nil {
		problems = append(problems, "无法创建技能目录 "+filepath.Join(dataDir, "skills")+"："+err.Error())
	}
	if cfg, err = config.LoadSkills(path); err != nil {
		return nil, nil, nil, err
	}

	discovered, scanProblems := skills.Discover(skillRoots(dataDir, cwd, home, cfg.ScansAgents()))
	problems = append(problems, scanProblems...)

	// 旧版的 "disabled": [...] 迁移成注册表：仍在磁盘上的技能照旧登记，禁用状态保留。
	if len(cfg.Legacy) > 0 && len(cfg.Skills) == 0 {
		migrated := &config.SkillsFile{Skills: make(map[string]config.SkillEntry, len(discovered))}
		for _, sk := range discovered {
			off := slices.ContainsFunc(cfg.Legacy, func(n string) bool { return strings.EqualFold(strings.TrimSpace(n), sk.Name) })
			migrated.Skills[sk.Name] = config.SkillEntry{Path: sk.Dir, Enable: !off}
		}
		if err := config.WriteSkills(path, migrated); err != nil {
			return nil, nil, nil, err
		}
		problems = append(problems, "skills.jsonc 已从旧的禁用列表迁移为注册表格式")
		if cfg, err = config.LoadSkills(path); err != nil {
			return nil, nil, nil, err
		}
	}

	// 新发现的技能自动登记；已有条目不覆盖（用户可能改过路径或开关）。
	added := 0
	for _, sk := range discovered {
		if _, ok := cfg.Entry(sk.Name); ok {
			continue
		}
		ok, err := config.AddSkill(path, sk.Name, sk.Dir)
		if err != nil {
			problems = append(problems, "自动注册技能 "+sk.Name+" 失败："+err.Error())
			continue
		}
		if ok {
			added++
		}
	}
	if added > 0 {
		if cfg, err = config.LoadSkills(path); err != nil {
			return nil, nil, nil, err
		}
	}

	// 按注册表逐个加载。名字以注册名为准：面板、开关和提示词里的名字必须一致。
	agents := agentSkillRoots(cwd, home)
	byName := make(map[string]skills.Skill, len(discovered))
	for _, sk := range discovered {
		byName[sk.Name] = sk
	}
	for _, name := range cfg.Names() {
		e := cfg.Skills[name]
		dir := strings.TrimSpace(e.Path)
		if dir == "" {
			sk, ok := byName[name]
			if !ok {
				problems = append(problems, fmt.Sprintf("技能 %q 没有写 path，扫描位置里也没找到", name))
				continue
			}
			dir = sk.Dir
		} else if !filepath.IsAbs(dir) {
			dir = filepath.Join(dataDir, dir)
		}
		if !cfg.ScansAgents() && slices.ContainsFunc(agents, func(root string) bool { return underDir(dir, root) }) {
			problems = append(problems, fmt.Sprintf("技能 %q 在 .agents 目录下，scanAgents 已关闭，未加载", name))
			continue
		}
		sk, warns, loadErr := skills.Load(dir, sourceFor(dir, dataDir, cwd, home, cfg.ScansAgents()))
		problems = append(problems, warns...)
		if loadErr != nil {
			problems = append(problems, fmt.Sprintf("技能 %q 未加载：%v", name, loadErr))
			continue
		}
		if sk.Name != name {
			problems = append(problems, fmt.Sprintf("技能注册名 %q 与 SKILL.md 里的 name %q 不一致，以注册名为准", name, sk.Name))
			sk.Name = name
		}
		list = append(list, sk)
	}
	return cfg, list, problems, nil
}

// skillInfos 把技能转成界面用的形状。带上路径是为了让 TUI 认出
// “模型正在读某个技能的正文”，在对话区标出来；Enabled 来自注册表的开关。
func skillInfos(list []skills.Skill, cfg *config.SkillsFile) []tui.SkillInfo {
	if len(list) == 0 {
		return nil
	}
	out := make([]tui.SkillInfo, len(list))
	for i, sk := range list {
		e, _ := cfg.Entry(sk.Name)
		out[i] = tui.SkillInfo{
			Name: sk.Name, Path: sk.Path, Desc: sk.Description,
			Source: string(sk.Source), Enabled: e.Enable,
		}
	}
	return out
}

// activeSkills 返回启用的技能；系统提示词里只列这些。
func (a *app) activeSkills() []skills.Skill {
	a.mu.Lock()
	list, cfg := a.skills, a.skillCfg
	a.mu.Unlock()
	out := make([]skills.Skill, 0, len(list))
	for _, sk := range list {
		if e, ok := cfg.Entry(sk.Name); ok && e.Enable {
			out = append(out, sk)
		}
	}
	return out
}

// skillInfosLocked 按当前注册表生成展示信息。
func (a *app) skillInfosLocked() []tui.SkillInfo {
	a.mu.Lock()
	list, cfg := a.skills, a.skillCfg
	a.mu.Unlock()
	return skillInfos(list, cfg)
}

// commitSkills 在锁内换上一批技能与注册表，供面板操作使用。
func (a *app) commitSkills(cfg *config.SkillsFile, list []skills.Skill) {
	a.mu.Lock()
	a.skills, a.skillCfg, a.skillsDirty = list, cfg, true
	a.mu.Unlock()
}

// reloadSkills 重新扫描并加载注册表，结果写回 app。
func (a *app) reloadSkills() (problems []string, err error) {
	cfg, list, problems, err := skillCatalog(a.dataDir, a.cwd, a.env.home)
	if err != nil {
		return nil, err
	}
	a.commitSkills(cfg, list)
	return problems, nil
}

// skillStore 把 app 适配成 tui.SkillPanel。
//
// 操作在 tea.Cmd 的 goroutine 里跑；完成后由 TUI 在空闲时调 applyMCP 重建 agent，
// 因为技能目录是系统提示词的一部分，改完必须重造 agent 才生效。
type skillStore struct{ a *app }

var _ tui.SkillPanel = skillStore{}

func (s skillStore) Skills() []tui.SkillInfo { return s.a.skillInfosLocked() }

// ScanAgents 报告是否加载 .agents/skills 下的技能。
func (s skillStore) ScanAgents() bool {
	s.a.mu.Lock()
	defer s.a.mu.Unlock()
	return s.a.skillCfg.ScansAgents()
}

// Trigger 手动触发一个技能：按名字找到已启用的技能，读出正文组成一条提问。
// 输入里的名字不是注册技能时返回 tui.ErrUnknownSkill，由 TUI 决定按普通提问处理。
func (s skillStore) Trigger(name, task string) (string, error) {
	a := s.a
	a.mu.Lock()
	list, cfg := a.skills, a.skillCfg
	a.mu.Unlock()

	var found *skills.Skill
	for i := range list {
		if list[i].Name == name {
			found = &list[i]
			break
		}
	}
	if found == nil {
		// 名字大小写不同也算同一个：技能名规范是小写，但用户手打时未必记得。
		for i := range list {
			if strings.EqualFold(list[i].Name, name) {
				found = &list[i]
				break
			}
		}
	}
	if found == nil {
		return "", tui.ErrUnknownSkill
	}
	if e, ok := cfg.Entry(found.Name); !ok || !e.Enable {
		return "", fmt.Errorf("技能 %s 已停用，用 /skills 打开后再试", found.Name)
	}
	body, err := skills.Body(*found)
	if err != nil {
		return "", err
	}
	return skills.Invocation(*found, body, task), nil
}

// SetSkill 启用或停用一个已注册的技能：写回 skills.jsonc。技能文件本身不动。
func (s skillStore) SetSkill(name string, enable bool) (string, error) {
	a := s.a
	if err := config.SetSkillEnabled(config.SkillsPath(a.dataDir), name, enable); err != nil {
		return "", err
	}
	cfg, err := config.LoadSkills(config.SkillsPath(a.dataDir))
	if err != nil {
		return "", err
	}
	a.commitSkills(cfg, a.skillList())
	action := "启用"
	if !enable {
		action = "停用"
	}
	return fmt.Sprintf("已%s技能 %s，下一轮对话生效", action, name), nil
}

// SetScanAgents 打开或关闭 .agents/skills 的自动加载，并立即重新扫描。
func (s skillStore) SetScanAgents(on bool) (string, error) {
	a := s.a
	if err := config.SetScanAgents(config.SkillsPath(a.dataDir), on); err != nil {
		return "", err
	}
	problems, err := a.reloadSkills()
	if err != nil {
		return "", err
	}
	notice := "已关闭 .agents/skills 的自动加载，其中的技能不再交给模型"
	if on {
		notice = "已开启 .agents/skills 的自动加载"
	}
	for _, p := range problems {
		notice += "\n" + p
	}
	return notice, nil
}

// Rescan 重新扫描技能目录：新技能自动注册，删掉的照旧留在注册表里。
func (s skillStore) Rescan() (string, error) {
	a := s.a
	problems, err := a.reloadSkills()
	if err != nil {
		return "", err
	}
	enabled := 0
	for _, sk := range a.skillInfosLocked() {
		if sk.Enabled {
			enabled++
		}
	}
	notice := fmt.Sprintf("已重新扫描技能：共 %d 个，启用 %d 个", len(a.skillInfosLocked()), enabled)
	for _, p := range problems {
		notice += "\n" + p
	}
	return notice, nil
}

// skillList 返回锁内的技能列表副本。
func (a *app) skillList() []skills.Skill {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]skills.Skill(nil), a.skills...)
}
