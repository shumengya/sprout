package tui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// EffortChoice 是 /effort 里的一个档位。
type EffortChoice struct {
	Name   string // auto、low、high 等
	Detail string // 这个档位会向服务端发送什么，用该协议的真实字段名描述
}

// Efforts 让 TUI 调整思考程度，由 main 提供实现。档位从 models.jsonc 读出，TUI 不认识具体参数。
type Efforts interface {
	// Choices 返回当前模型的档位，从弱到强；为空表示当前模型没有声明 effort。
	Choices() []EffortChoice
	// Current 返回当前档位。
	Current() string
	// Set 切换档位，从下一次模型请求开始生效，可以在生成中调用。返回给用户看的提示。
	Set(name string) (string, error)
	// Unsupported 说明当前模型为什么不能调节（Choices 为空时使用）。
	Unsupported() string
}

// effortPicker 是 /effort 的横向档位条：替换输入框，左右键选择，回车确认。
type effortPicker struct {
	items   []EffortChoice
	cursor  int
	current int // 打开时所在的档位
}

// effortCmd 处理 /effort。无参数打开档位条；带参数直接切换。
func (m *Model) effortCmd(arg string) {
	s := m.opts.Efforts
	if s == nil {
		m.add(&entry{kind: kindNotice, text: "当前不能调整思考程度（没有可用的 agent）。"})
		return
	}
	items := s.Choices()
	if len(items) == 0 {
		m.add(&entry{kind: kindNotice, text: s.Unsupported()})
		return
	}
	if arg == "" {
		cur := max(slices.IndexFunc(items, func(c EffortChoice) bool { return c.Name == s.Current() }), 0)
		m.effort = &effortPicker{items: items, cursor: cur, current: cur}
		m.status = ""
		return
	}
	m.setEffort(strings.ToLower(arg))
}

// setEffort 切换档位并刷新信息栏。
func (m *Model) setEffort(name string) {
	notice, err := m.opts.Efforts.Set(name)
	if err != nil {
		m.add(&entry{kind: kindError, text: "调整思考程度失败：" + err.Error()})
		return
	}
	m.opts.Info.Effort = m.opts.Efforts.Current()
	m.add(&entry{kind: kindNotice, text: notice})
}

// effortKey 在档位条打开时处理按键，返回 true 表示已消费。
func (m *Model) effortKey(k tea.KeyPressMsg) bool {
	p := m.effort
	if p == nil {
		return false
	}
	switch k.String() {
	case "esc", "ctrl+c":
		m.effort = nil
		m.status = "已取消调整思考程度"
	case "left", "h", "up", "ctrl+p", "shift+tab":
		p.cursor = max(p.cursor-1, 0)
	case "right", "l", "down", "ctrl+n", "tab":
		p.cursor = min(p.cursor+1, len(p.items)-1)
	case "home":
		p.cursor = 0
	case "end":
		p.cursor = len(p.items) - 1
	case "enter":
		name := p.items[p.cursor].Name
		m.effort = nil
		if p.cursor == p.current {
			m.status = "思考程度保持 " + name
			return true
		}
		m.setEffort(name)
		m.follow = true
	default:
		// 数字键直接跳到第 n 个档位，不用连按方向键。
		if r := []rune(k.Text); len(r) == 1 && r[0] >= '1' && r[0] <= '9' {
			if i := int(r[0] - '1'); i < len(p.items) {
				p.cursor = i
			}
		}
	}
	return true
}

// effortView 渲染档位条：标题、轨道、档位名、当前选中档位的说明。
//
//	思考程度  ← → 选择  Enter 确认  Esc 取消
//	  更快 ──────●────────── 更深入
//	       auto  low  [high]  max
//	  output_config.effort=high
func (m *Model) effortView(w int) string {
	p := m.effort
	var b strings.Builder
	b.WriteString(styInfoLabel.Render("思考程度  ← → 选择  1-9 跳转  Enter 确认  Esc 取消"))
	b.WriteString("\n")

	// 档位名一行，记下每个档位中点所在的列，轨道上的标记对齐到这里。
	const indent, gap = 2, 3
	var names strings.Builder
	names.WriteString(strings.Repeat(" ", indent))
	mids := make([]int, len(p.items))
	col := indent
	for i, it := range p.items {
		if i > 0 {
			names.WriteString(strings.Repeat(" ", gap))
			col += gap
		}
		label := it.Name
		width := ansi.StringWidth(label)
		mids[i] = col + width/2
		switch {
		case i == p.cursor:
			label = styEffortSel.Render(label)
		case i == p.current:
			label = styOK.Render(label)
		default:
			label = styFaint.Render(label)
		}
		names.WriteString(label)
		col += width
	}

	track := []rune(strings.Repeat("─", max(col-indent, 1)))
	if i := mids[p.cursor] - indent; i >= 0 && i < len(track) {
		track[i] = '●'
	}
	b.WriteString(styFaint.Render(strings.Repeat(" ", indent) + string(track)))
	b.WriteString(styFaint.Render("  更快 → 更深入"))
	b.WriteString("\n")
	b.WriteString(names.String())
	b.WriteString("\n")

	detail := "  " + p.items[p.cursor].Detail
	if p.cursor == p.current {
		detail += "  ← 当前"
	}
	b.WriteString(styFaint.Render(detail))

	lines := strings.Split(b.String(), "\n")
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, w, "…")
	}
	return strings.Join(lines, "\n")
}
