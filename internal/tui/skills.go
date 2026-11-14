package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// SkillPanel 让 TUI 读取并修改技能的开关，由 main 提供实现。
//
// 停用只影响“是否把技能元数据交给模型”，磁盘上的技能文件不动。改完同样要重建 agent
// （系统提示词里带着技能目录），所以操作完成后 TUI 会自己触发一次 ApplyMCP，
// 面板的列表也从刷新后的 Info 取。
type SkillPanel interface {
	// Skills 返回全部已发现的技能（含停用的）。重新扫描后返回新的结果。
	Skills() []SkillInfo
	// SetSkill 启用或停用一个技能，返回给用户看的提示。
	SetSkill(name string, enable bool) (string, error)
	// Rescan 重新扫描技能目录，返回给用户看的提示（含扫描到的问题）。
	Rescan() (string, error)
}

// skillPicker 是 /skills 的技能面板：替换输入框，↑↓ 选择，空格开关，r 重新扫描。
type skillPicker struct {
	skills []SkillInfo
	cursor int
	offset int
	busy   string // 非空表示正在执行某个操作，期间不再接受新操作
	note   string // 最近一次操作的结果
	noteOK bool
}

// selected 返回当前高亮的技能；列表为空时 ok 为 false。
func (p *skillPicker) selected() (SkillInfo, bool) {
	if len(p.skills) == 0 {
		return SkillInfo{}, false
	}
	return p.skills[min(p.cursor, len(p.skills)-1)], true
}

// move 上下移动光标，不越界。
func (p *skillPicker) move(d int) {
	n := len(p.skills)
	p.cursor = min(max(p.cursor+d, 0), max(n-1, 0))
}

// enabledCount 数启用的技能。
func (p *skillPicker) enabledCount() int {
	n := 0
	for _, sk := range p.skills {
		if sk.Enabled {
			n++
		}
	}
	return n
}

// skillsCmd 处理 /skills。没有实现时说明为什么不能用。
func (m *Model) skillsCmd() {
	s := m.opts.Skills
	switch {
	case s == nil:
		m.add(&entry{kind: kindNotice, text: "当前不能管理技能（没有可用的 agent）。"})
	case m.busy():
		m.status = "正在生成、恢复或压缩中，暂时不能管理技能"
	default:
		m.skillPick = &skillPicker{skills: s.Skills()}
		m.status = ""
	}
}

// skillDoneMsg 是面板操作的完成消息。
type skillDoneMsg struct {
	notice string
	err    error
}

// skillKey 在面板打开时处理按键，返回 true 表示已消费。
func (m *Model) skillKey(k tea.KeyPressMsg) (bool, tea.Cmd) {
	p := m.skillPick
	if p == nil {
		return false, nil
	}
	key := k.String()
	if p.busy != "" { // 操作进行中：只允许关闭面板，避免并发改同一份状态
		if key == "esc" || key == "ctrl+c" {
			m.status = "技能操作还在进行，等它结束再关闭面板"
		}
		return true, nil
	}
	sk, hasSkill := p.selected()
	switch key {
	case "esc", "ctrl+c":
		m.skillPick = nil
		m.status = "已关闭技能面板"
	case "up", "ctrl+p", "k":
		p.cursor--
	case "down", "ctrl+n", "j":
		p.cursor++
	case "pgup":
		p.move(-m.skillRows())
	case "pgdown":
		p.move(m.skillRows())
	case "home":
		p.cursor = 0
	case "end":
		p.cursor = 1 << 30
	case "space", "enter": // bubbletea v2 里空格的 String() 是 "space"
		if !hasSkill {
			return true, nil
		}
		enable := !sk.Enabled
		label := "启用"
		if !enable {
			label = "停用"
		}
		return true, m.skillDo(fmt.Sprintf("正在%s技能 %s…", label, sk.Name), func(s SkillPanel) (string, error) {
			return s.SetSkill(sk.Name, enable)
		})
	case "r":
		return true, m.skillDo("正在重新扫描技能目录…", func(s SkillPanel) (string, error) { return s.Rescan() })
	}
	p.cursor = min(max(p.cursor, 0), max(len(p.skills)-1, 0))
	return true, nil
}

// skillDo 在 tea.Cmd 里执行一个面板操作。写状态文件与扫描目录都不该卡住 Update。
func (m *Model) skillDo(status string, op func(SkillPanel) (string, error)) tea.Cmd {
	s := m.opts.Skills
	m.skillPick.busy = status
	m.status = status
	return func() tea.Msg {
		notice, err := op(s)
		return skillDoneMsg{notice: notice, err: err}
	}
}

// onSkillDone 处理面板操作结果。成功时让 Update 的收尾去跑 applyMCP：
// 系统提示词里的技能目录跟着变，agent 只能在主线程、空闲时重建。
func (m *Model) onSkillDone(msg skillDoneMsg) {
	if msg.err == nil {
		m.mcpDone = m.opts.ApplyMCP != nil // 与 MCP 共用“空闲时重建 agent”的标记
	}
	p := m.skillPick
	if p == nil {
		return // 面板已经关了，结果只影响 Info（由 applyMCP 刷新）
	}
	p.busy = ""
	if msg.err != nil {
		p.note, p.noteOK = msg.err.Error(), false
		return
	}
	p.note, p.noteOK = msg.notice, true
}

// syncSkills 把刷新后的技能列表交给打开着的面板。
func (m *Model) syncSkills() {
	if m.skillPick == nil {
		return
	}
	m.skillPick.skills = m.opts.Info.Skills
	m.skillPick.cursor = min(max(m.skillPick.cursor, 0), max(len(m.skillPick.skills)-1, 0))
}

// skillRows 是面板列表最多显示的行数：屏幕越高显示越多，3 到 14 行。
func (m *Model) skillRows() int { return min(max(m.h-14, 3), 14) }

// skillView 渲染技能面板，替换输入框的位置。
func (m *Model) skillView(w int) string {
	p := m.skillPick
	var lines []string

	lines = append(lines, spread(
		styBrowseTitle.Render("技能"),
		styFaint.Render(fmt.Sprintf("已启用 %d/%d", p.enabledCount(), len(p.skills))), w))
	lines = append(lines, styFaint.Render("↑↓ 选择 · 空格 / Enter 开关 · r 重新扫描 · Esc 关闭"))

	if len(p.skills) == 0 {
		lines = append(lines, styFaint.Render("  没有发现技能，放进数据目录的 skills/ 或项目里的 .agents/skills/ 后按 r。"))
	}
	if len(p.skills) > 0 {
		p.cursor = min(p.cursor, len(p.skills)-1)
		height := m.skillRows()
		if p.cursor < p.offset {
			p.offset = p.cursor
		}
		if p.cursor >= p.offset+height {
			p.offset = p.cursor - height + 1
		}
		p.offset = max(min(p.offset, len(p.skills)-height), 0)
		for i := p.offset; i < min(p.offset+height, len(p.skills)); i++ {
			lines = append(lines, p.row(p.skills[i], i == p.cursor, w))
		}
	}

	foot := ""
	if len(p.skills) > 0 {
		foot = styFaint.Render(fmt.Sprintf("(%d/%d)", p.cursor+1, len(p.skills)))
	}
	switch {
	case p.busy != "":
		foot += "  " + styWarn.Render(p.busy)
	case p.note != "" && p.noteOK:
		foot += "  " + styOK.Render(p.note)
	case p.note != "":
		foot += "  " + styWarn.Render(p.note)
	}
	if foot != "" {
		lines = append(lines, foot)
	}

	for i, l := range lines {
		lines[i] = ansi.Truncate(l, w, "…")
	}
	return strings.Join(lines, "\n")
}

// row 渲染一行技能：开关标记、名字、来源，右边是 description 的首行。
func (p *skillPicker) row(sk SkillInfo, sel bool, w int) string {
	mark := styFaint.Render("○")
	if sk.Enabled {
		mark = styOK.Render("●")
	}
	left := mark + " " + sk.Name
	if sk.Source != "" {
		left += styFaint.Render("  " + sk.Source)
	}
	// description 往往很长，直接跟在后面截断，比右对齐时整段放不下而被丢掉更有用。
	if sk.Desc != "" {
		left += "  " + styFaint.Render(firstLine(sk.Desc))
	}
	if sel {
		return ansi.Truncate(styReverse.Render("❯ "+left), w, "…")
	}
	return ansi.Truncate("  "+left, w, "…")
}
