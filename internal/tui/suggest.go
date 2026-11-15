package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// slashCommand 是一个可补全的斜杠命令。slash() 里的每个命令都要在这里登记，commands_test 保证两边一致。
type slashCommand struct {
	name string
	args string // 参数提示，如 "[档位]"；空表示不带参数
	desc string
}

// slashCommands 按常用程度排列，候选列表就按这个顺序显示。
var slashCommands = []slashCommand{
	{"/help", "", "显示快捷键与命令说明"},
	{"/model", "[provider/model]", "切换模型，保留对话历史"},
	{"/effort", "[档位]", "调整思考程度"},
	{"/think", "", "展开 / 折叠全部思考过程"},
	{"/new", "", "开始新会话"},
	{"/compact", "[说明]", "把前面的对话总结成摘要，腾出上下文"},
	{"/resume", "", "浏览并恢复历史会话（可搜索、重命名、删除）"},
	{"/image", "[路径|clear]", "附加图片到下一条消息"},
	{"/tools", "", "查看检测到的终端工具"},
	{"/mcp", "", "管理 MCP 服务端与工具开关"},
	{"/skills", "", "管理技能开关、重新扫描技能目录"},
	{"/mouse", "", "开关鼠标上报"},
	{"/install-sprout", "", "把当前程序注册成全局 sprout 命令"},
	{"/uninstall-sprout", "", "卸载全局 sprout 命令"},
	{"/update", "", "更新到最新发布版本"},
	{"/quit", "", "退出"},
	{"/exit", "", "退出（同 /quit）"},
}

// suggestion 是候选列表里的一项。
type suggestion struct {
	value   string // 选中后输入框的完整内容
	label   string
	hint    string // 跟在 label 后面的淡色参数提示
	desc    string
	current bool // 当前模型 / 当前档位
}

// suggestState 是输入框上方的候选列表。输入框内容一变就重新计算，选中项回到第一项。
type suggestState struct {
	items    []suggestion
	sel      int
	moved    bool   // 用户用 ↑↓ 选过：回车时一定用选中项
	token    string // 正在补全的那一段（命令名或参数）
	forValue string // items 对应的输入内容
	off      string // 按 Esc 关闭时的输入内容；内容不变就不再弹出
}

// suggestMaxRows 是候选列表最多显示的行数（不含底部的按键提示），再多就在列表内部滚动。
const suggestMaxRows = 6

// syncSuggest 按输入框当前内容重算候选。每次 Update 之后调用。
func (m *Model) syncSuggest() {
	s := &m.sug
	v := m.in.Value()
	if v != s.forValue {
		s.forValue, s.sel, s.moved = v, 0, false
	}
	s.items, s.token = nil, ""
	if m.picker != nil || m.effort != nil || m.browser != nil || m.mcpPicker != nil || m.skillPick != nil || v == s.off {
		return
	}
	s.items, s.token = m.suggestionsFor(v)
	s.sel = min(s.sel, max(len(s.items)-1, 0))
}

// suggestionsFor 计算输入 v 的候选。只在单行、以 / 开头时补全：
// 没有空格时补全命令名；"/effort xx"、"/model xx" 补全参数。其他情况（包括 /etc/hosts 这类路径）没有候选。
func (m *Model) suggestionsFor(v string) ([]suggestion, string) {
	if !strings.HasPrefix(v, "/") || strings.ContainsAny(v, "\r\n\t") {
		return nil, ""
	}
	name, arg, hasArg := strings.Cut(v, " ")
	if !hasArg {
		low := strings.ToLower(name)
		var out []suggestion
		for _, c := range slashCommands {
			if !strings.HasPrefix(c.name, low) {
				continue
			}
			val := c.name
			if c.args != "" {
				val += " " // 补全后紧接着补参数
			}
			out = append(out, suggestion{value: val, label: c.name, hint: c.args, desc: c.desc})
		}
		// 技能排在命令后面：/技能名 也能手动触发（见 skillInvocation）。
		// 与内置命令同名的技能不会被选到，提交时命令优先。
		out = append(out, m.skillSuggestions(low)...)
		return out, name
	}
	if strings.Contains(arg, " ") {
		return nil, ""
	}
	low := strings.ToLower(arg)
	var out []suggestion
	switch name {
	case "/effort":
		if e := m.opts.Efforts; e != nil {
			cur := e.Current()
			for _, c := range e.Choices() {
				if strings.HasPrefix(c.Name, low) {
					out = append(out, suggestion{value: name + " " + c.Name, label: c.Name, desc: c.Detail, current: c.Name == cur})
				}
			}
		}
	case "/model":
		if ms := m.opts.Models; ms != nil {
			// 模型引用很长，按包含匹配，"/model opus" 就能找到 xxx/claude-opus-5-5。
			for _, it := range ms.List() {
				if strings.Contains(strings.ToLower(it.Ref), low) {
					out = append(out, suggestion{value: name + " " + it.Ref, label: it.Ref, desc: it.Detail, current: it.Current})
				}
			}
		}
	}
	return out, arg
}

// skillSuggestions 列出名字以 low 开头的启用技能，值补成 "/名字 "，方便接着写任务。
func (m *Model) skillSuggestions(low string) []suggestion {
	if m.opts.Skills == nil {
		return nil
	}
	var out []suggestion
	for _, sk := range m.opts.Skills.Skills() {
		name := "/" + sk.Name
		if !sk.Enabled || !strings.HasPrefix(strings.ToLower(name), low) {
			continue
		}
		if slices.ContainsFunc(slashCommands, func(c slashCommand) bool { return c.name == name }) {
			continue
		}
		out = append(out, suggestion{value: name + " ", label: name, hint: "[任务]", desc: "触发技能 " + sk.Name})
	}
	return out
}

// suggestKey 在候选列表可见时处理按键，返回 true 表示已消费。
func (m *Model) suggestKey(k tea.KeyPressMsg) (bool, tea.Model, tea.Cmd) {
	s := &m.sug
	if len(s.items) == 0 {
		return false, m, nil
	}
	switch k.String() {
	case "tab":
		m.in.SetValue(s.items[s.sel].value)
	case "up", "ctrl+p", "shift+tab":
		s.sel = (s.sel - 1 + len(s.items)) % len(s.items)
		s.moved = true
	case "down", "ctrl+n":
		s.sel = (s.sel + 1) % len(s.items)
		s.moved = true
	case "esc":
		s.off = m.in.Value()
	case "enter":
		if v := m.pickOnEnter(); v != "" {
			m.in.SetValue(v)
		}
		model, cmd := m.submit()
		return true, model, cmd
	default:
		return false, m, nil
	}
	return true, m, nil
}

// pickOnEnter 决定回车时是否换成选中的候选，返回空串表示按输入原样提交。
//
// 输入了一部分（/eff）就执行选中项；输入正好是某个候选（/model、/effort high）就按原样执行，
// 不被排在前面的其他候选顶掉；只敲了 "/effort " 还没写参数时也按原样执行（打开档位条），
// 除非用户用 ↑↓ 明确选了一项。
func (m *Model) pickOnEnter() string {
	s := m.sug
	typed := strings.TrimSpace(m.in.Value())
	sel := strings.TrimSpace(s.items[s.sel].value)
	if s.moved {
		return sel
	}
	if s.token == "" {
		return ""
	}
	for _, it := range s.items {
		if strings.TrimSpace(it.value) == typed {
			return ""
		}
	}
	return sel
}

// suggestRows 是候选列表占的行数；没有候选时为 0。
func (m *Model) suggestRows() int {
	if len(m.sug.items) == 0 {
		return 0
	}
	return strings.Count(m.suggestView(max(m.w, 1)), "\n") + 1
}

// suggestView 渲染候选列表，显示在输入框上方：
//
//	❯ /effort  [档位]   调整思考程度
//	  /exit             退出（同 /quit）
//	  Tab 补全 · ↑↓ 选择 · Enter 执行 · Esc 关闭
func (m *Model) suggestView(w int) string {
	s := m.sug
	n := len(s.items)
	start := max(s.sel-suggestMaxRows/2, 0)
	end := min(start+suggestMaxRows, n)
	start = max(min(start, end-suggestMaxRows), 0)

	// 让说明列对齐：取可见项里最宽的“名称 + 参数”。
	col := 0
	for _, it := range s.items[start:end] {
		col = max(col, ansi.StringWidth(labelText(it)))
	}

	var b strings.Builder
	for i := start; i < end; i++ {
		it := s.items[i]
		pad := strings.Repeat(" ", col-ansi.StringWidth(labelText(it))+3)
		label := it.label
		if it.hint != "" {
			label += " " + styFaint.Render(it.hint)
		}
		line := "  " + label
		if i == s.sel {
			line = "❯ " + styEffortSel.Render(labelText(it))
		}
		line += pad + styFaint.Render(it.desc)
		if it.current {
			line += styOK.Render("  ← 当前")
		}
		b.WriteString(ansi.Truncate(line, w, "…"))
		b.WriteString("\n")
	}
	hint := "  Tab 补全 · ↑↓ 选择 · Enter 执行 · Esc 关闭"
	if n > suggestMaxRows {
		hint += fmt.Sprintf("  (%d/%d)", s.sel+1, n)
	}
	b.WriteString(ansi.Truncate(styFaint.Render(hint), w, "…"))
	return b.String()
}

func labelText(it suggestion) string {
	if it.hint == "" {
		return it.label
	}
	return it.label + " " + it.hint
}
