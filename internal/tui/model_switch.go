package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// ModelChoice 是 /model 列表里的一项，由 main 从 models.jsonc 枚举出来。
type ModelChoice struct {
	Ref      string // "provider/model"
	Provider string
	Model    string
	Protocol string // 协议名，如 openai-chat
	Detail   string // 附加说明，如 "上下文 128k · 工具"
	Current  bool   // 是否是当前正在使用的模型
}

// SwitchResult 是切换模型的结果：新的 Info 用来刷新信息栏，Notice 显示在对话区。
type SwitchResult struct {
	Info   Info
	Notice string
}

// Models 让 TUI 切换模型，由 main 提供实现。TUI 不直接依赖 provider 构造与配置文件。
type Models interface {
	// List 返回 models.jsonc 里的全部模型，按引用排序。
	List() []ModelChoice
	// Switch 切换到 ref 指向的模型：重建 provider 与 agent，保留对话历史。
	// 调用方必须保证此刻没有 Run 在进行。它可能阻塞，所以 TUI 在 tea.Cmd 里调用。
	Switch(ref string) (*SwitchResult, error)
	// Current 返回当前模型的引用。
	Current() string
}

// switchDoneMsg 是切换完成后发回 Update 的消息。
type switchDoneMsg struct {
	res *SwitchResult
	err error
}

// picker 是 /model 的全屏内联选择器：替换输入框，不遮挡对话区。
type picker struct {
	items  []ModelChoice
	filter string
	cursor int
}

// visible 返回按过滤词筛选后的选项下标。过滤词按空白拆开，每一项都要命中引用或协议。
func (p *picker) visible() []int {
	terms := strings.Fields(strings.ToLower(p.filter))
	var out []int
	for i, it := range p.items {
		hay := strings.ToLower(it.Ref + " " + it.Protocol)
		ok := true
		for _, t := range terms {
			if !strings.Contains(hay, t) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, i)
		}
	}
	return out
}

// selected 返回当前高亮的选项下标；没有匹配项时 ok 为 false。
func (p *picker) selected() (int, bool) {
	v := p.visible()
	if len(v) == 0 {
		return 0, false
	}
	return v[min(p.cursor, len(v)-1)], true
}

// move 上下移动光标，不越界。
func (p *picker) move(d int) {
	n := len(p.visible())
	if n == 0 {
		p.cursor = 0
		return
	}
	p.cursor = min(max(p.cursor+d, 0), n-1)
}

// modelCmd 处理 /model。无参数时打开选择器；带参数时按引用或序号直接切换。
func (m *Model) modelCmd(arg string) tea.Cmd {
	s := m.opts.Models
	switch {
	case s == nil:
		m.add(&entry{kind: kindNotice, text: "当前不能切换模型（没有可用的 agent）。"})
	case m.running || m.resuming:
		m.status = "正在生成或恢复中，暂时不能切换模型"
	case arg == "":
		m.picker = &picker{items: s.List()}
		m.status = ""
	default:
		return m.doSwitch(arg)
	}
	return nil
}

// doSwitch 异步切换模型。切换期间禁止发送，避免新提问落进半换的 agent。
func (m *Model) doSwitch(ref string) tea.Cmd {
	m.switching = true
	m.status = "正在切换模型…"
	src := m.opts.Models
	return func() tea.Msg {
		res, err := src.Switch(ref)
		return switchDoneMsg{res, err}
	}
}

// onSwitched 处理切换结果：成功就刷新信息栏并提示；失败只报错，当前模型保持不变。
func (m *Model) onSwitched(msg switchDoneMsg) {
	m.switching, m.status = false, ""
	if msg.err != nil {
		m.add(&entry{kind: kindError, text: "切换模型失败：" + msg.err.Error()})
		return
	}
	m.opts.Info.Model = msg.res.Info.Model
	m.opts.Info.Provider = msg.res.Info.Provider
	m.opts.Info.Tools = msg.res.Info.Tools
	m.opts.Info.Vision = msg.res.Info.Vision
	m.opts.Info.Effort = msg.res.Info.Effort
	m.add(&entry{kind: kindNotice, text: msg.res.Notice})
}

// pickerKey 在选择器打开时处理按键，返回 true 表示已消费。
func (m *Model) pickerKey(k tea.KeyPressMsg) (handled bool, cmd tea.Cmd) {
	if m.picker == nil {
		return false, nil
	}
	switch k.String() {
	case "esc", "ctrl+c":
		m.picker = nil
		m.status = "已取消选择模型"
		return true, nil
	case "up", "ctrl+p":
		m.picker.move(-1)
	case "down", "ctrl+n":
		m.picker.move(1)
	case "enter":
		idx, ok := m.picker.selected()
		if !ok {
			m.status = "没有匹配的模型"
			return true, nil
		}
		ref := m.pickerRef(idx) // 先取引用，再关闭选择器
		m.picker = nil
		return true, m.doSwitch(ref)
	case "backspace":
		r := []rune(m.picker.filter)
		if len(r) > 0 {
			m.picker.filter = string(r[:len(r)-1])
			m.picker.cursor = 0
		}
	case "ctrl+u":
		m.picker.filter = ""
		m.picker.cursor = 0
	case "ctrl+d":
		return true, nil // 选择器里 Ctrl+D 不退出，避免误触
	default:
		if k.Text != "" {
			m.picker.filter += k.Text
			m.picker.cursor = 0
			return true, nil
		}
		return true, nil
	}
	return true, nil
}

// pickerRef 返回第 i 项在 items 里的引用。直接选中的项可能是过滤后的下标。
func (m *Model) pickerRef(i int) string {
	if i >= 0 && i < len(m.picker.items) {
		return m.picker.items[i].Ref
	}
	return ""
}

// pickerView 渲染选择器，替换输入框位置；高度与输入框一致时保持底部布局不变。
func (m *Model) pickerView(w, maxRows int) string {
	p := m.picker
	var b strings.Builder
	head := "选择模型"
	if p.filter != "" {
		head += "  过滤：" + p.filter
	}
	b.WriteString(styInfoLabel.Render(head))
	b.WriteString("\n")

	v := p.visible()
	if len(v) == 0 {
		b.WriteString(styWarn.Render("  没有匹配的模型"))
	} else {
		cur := min(p.cursor, len(v)-1)
		// 只显示光标附近的一段，长列表不至于把对话区挤没。
		start := max(cur-maxRows/2, 0)
		end := min(start+maxRows, len(v))
		start = max(min(start, end-maxRows), 0)
		for i := start; i < end; i++ {
			it := p.items[v[i]]
			line := "  " + it.Ref
			if it.Detail != "" {
				line += styFaint.Render("  " + it.Detail)
			}
			if it.Current {
				line += styOK.Render("  ← 当前")
			}
			if i == cur {
				line = styReverse.Render("❯ " + it.Ref + "  " + it.Protocol)
				if it.Current {
					line += styFaint.Render("  ← 当前")
				}
			}
			b.WriteString(ansi.Truncate(line, w, "…"))
			b.WriteString("\n")
		}
		if len(v) > maxRows {
			b.WriteString(styFaint.Render(fmt.Sprintf("  (%d/%d)", cur+1, len(v))))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
