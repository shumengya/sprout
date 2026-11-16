package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Ask 是 MCP 服务端通过 agent 向用户要的输入：一份表单（Fields 非空），或一次纯确认（Fields 为空）。
// TUI 只管展示和收集，不认识 MCP；字段由 main 从服务端给的 JSON Schema 转换而来。
type Ask struct {
	Server  string
	Title   string
	Message string
	Fields  []AskField
	// Reply 容量必须为 1：TUI 恰好发送一次，之后不再碰它。
	Reply chan<- AskAnswer
}

// AskField 是表单里的一个输入项。
type AskField struct {
	Name     string
	Title    string
	Desc     string
	Kind     string   // string、number、integer、boolean、enum
	Choices  []string // enum 的取值
	Labels   []string // enum 的展示名，与 Choices 一一对应；可为空
	Required bool
	Default  any
}

// 用户对 Ask 的处理结果。
const (
	AskAccept  = "accept"
	AskDecline = "decline"
	AskCancel  = "cancel"
)

// AskAnswer 是用户的回答。只有 AskAccept 带 Values，类型已按字段转换好（string、float64、int64、bool）。
type AskAnswer struct {
	Action string
	Values map[string]any
}

// askState 是正在作答的那一份。bufs 存文本类字段的输入，picks 存布尔与枚举的选中项（-1 表示未选）。
type askState struct {
	ask    *Ask
	cursor int
	bufs   []string
	picks  []int
	err    string
}

// askMsg 表示收到一份新的输入请求。
type askMsg struct{ a *Ask }

// askCmd 等下一份请求。每处理完一份要重新挂起，所以 Update 里会再调用它。
func (m *Model) askCmd() tea.Cmd {
	ch := m.opts.Asks
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case a := <-ch:
			return askMsg{a}
		case <-m.quit:
			return nil
		}
	}
}

// onAsk 展示请求；已经有一份在作答时排队，按先后顺序依次问。
func (m *Model) onAsk(a *Ask) {
	if m.ask != nil {
		m.askQueue = append(m.askQueue, a)
		return
	}
	m.openAsk(a)
}

func (m *Model) openAsk(a *Ask) {
	st := &askState{ask: a, bufs: make([]string, len(a.Fields)), picks: make([]int, len(a.Fields))}
	for i, f := range a.Fields {
		st.picks[i] = -1
		switch f.Kind {
		case "boolean":
			def, _ := f.Default.(bool)
			st.picks[i] = map[bool]int{false: 0, true: 1}[def]
		case "enum":
			for j, c := range f.Choices {
				if s, ok := f.Default.(string); ok && s == c {
					st.picks[i] = j
				}
			}
			if st.picks[i] < 0 && f.Required && len(f.Choices) > 0 {
				st.picks[i] = 0
			}
		default:
			if f.Default != nil {
				st.bufs[i] = fmt.Sprint(f.Default)
			}
		}
	}
	m.ask = st
	m.status = ""
}

// finishAsk 把回答交给等待的一方，并接着问队列里的下一份。
func (m *Model) finishAsk(ans AskAnswer) {
	if m.ask != nil {
		m.ask.ask.Reply <- ans
	}
	m.ask = nil
	if len(m.askQueue) > 0 {
		next := m.askQueue[0]
		m.askQueue = m.askQueue[1:]
		m.openAsk(next)
	}
}

// cancelAsks 在界面退出时取消所有还没答的请求，让服务端那边的调用尽快返回。
func (m *Model) cancelAsks() {
	for m.ask != nil {
		m.finishAsk(AskAnswer{Action: AskCancel})
	}
}

// askKey 在输入请求打开时处理按键，返回 true 表示已消费。
func (m *Model) askKey(k tea.KeyPressMsg) bool {
	st := m.ask
	if st == nil {
		return false
	}
	st.err = ""
	switch k.String() {
	case "esc":
		m.finishAsk(AskAnswer{Action: AskCancel})
	case "ctrl+c":
		m.finishAsk(AskAnswer{Action: AskDecline})
	case "up", "ctrl+p":
		st.cursor = max(st.cursor-1, 0)
	case "down", "ctrl+n":
		st.cursor = min(st.cursor+1, len(st.ask.Fields))
	case "left", "h":
		m.askStep(st, -1)
	case "right", "l":
		m.askStep(st, 1)
	case "enter":
		if st.cursor == len(st.ask.Fields) {
			m.submitAsk(st)
		} else if f := st.ask.Fields[st.cursor]; f.Kind == "boolean" || f.Kind == "enum" {
			m.askStep(st, 1)
		}
	case "backspace":
		if i := st.cursor; i < len(st.ask.Fields) && st.ask.Fields[i].Kind != "boolean" && st.ask.Fields[i].Kind != "enum" && len(st.bufs[i]) > 0 {
			r := []rune(st.bufs[i])
			st.bufs[i] = string(r[:len(r)-1])
		}
	default:
		if i := st.cursor; i < len(st.ask.Fields) && st.ask.Fields[i].Kind != "boolean" && st.ask.Fields[i].Kind != "enum" {
			if t := k.Text; t != "" && !strings.ContainsAny(t, "\n\r\t") {
				st.bufs[i] += t
			}
		}
	}
	return true
}

// askStep 在布尔与枚举字段上切换选项。
func (m *Model) askStep(st *askState, dir int) {
	if st.cursor >= len(st.ask.Fields) {
		return
	}
	f := st.ask.Fields[st.cursor]
	switch f.Kind {
	case "boolean":
		st.picks[st.cursor] = (st.picks[st.cursor] + dir + 2) % 2
	case "enum":
		if n := len(f.Choices); n > 0 {
			st.picks[st.cursor] = (st.picks[st.cursor] + dir + n) % n
		}
	}
}

// submitAsk 校验并提交。必填没填或格式不对时停在表单上，不把半截答案发给服务端。
func (m *Model) submitAsk(st *askState) {
	values := map[string]any{}
	for i, f := range st.ask.Fields {
		v, filled, err := askValue(f, st.bufs[i], st.picks[i])
		if err != "" {
			st.err = err
			st.cursor = i
			return
		}
		if filled {
			values[f.Name] = v
		}
	}
	m.finishAsk(AskAnswer{Action: AskAccept, Values: values})
}

// askValue 把一个字段的输入转成 schema 要求的类型。filled 为 false 表示用户没填（只对非必填合法）。
func askValue(f AskField, buf string, pick int) (any, bool, string) {
	switch f.Kind {
	case "boolean":
		if pick < 0 {
			if f.Required {
				return nil, false, fieldTitle(f) + "：必填"
			}
			return nil, false, ""
		}
		return pick == 1, true, ""
	case "enum":
		if pick < 0 || pick >= len(f.Choices) {
			if f.Required {
				return nil, false, fieldTitle(f) + "：必填"
			}
			return nil, false, ""
		}
		return f.Choices[pick], true, ""
	}
	if strings.TrimSpace(buf) == "" {
		if f.Required {
			return nil, false, fieldTitle(f) + "：必填"
		}
		return nil, false, ""
	}
	switch f.Kind {
	case "integer":
		n, err := strconv.ParseInt(strings.TrimSpace(buf), 10, 64)
		if err != nil {
			return nil, false, fieldTitle(f) + "：需要整数"
		}
		return n, true, ""
	case "number":
		n, err := strconv.ParseFloat(strings.TrimSpace(buf), 64)
		if err != nil {
			return nil, false, fieldTitle(f) + "：需要数字"
		}
		return n, true, ""
	}
	return buf, true, ""
}

func fieldTitle(f AskField) string {
	if f.Title != "" {
		return f.Title
	}
	return f.Name
}

// askView 渲染输入请求。
//
//	MCP 服务端 fs 请求输入  ↑↓ 选择  Esc 取消  Ctrl+C 拒绝
//	  请填写部署参数
//	> 环境（必填）  [prod_]
//	  数量          3
//	  确认          是 / 否
//	  级别          dev  [staging]  prod
//	  [提交]
func (m *Model) askView(w int) string {
	st := m.ask
	a := st.ask
	var b strings.Builder
	b.WriteString(styInfoLabel.Render("MCP 服务端 " + a.Server + " 请求输入  ↑↓ 选择  ← → 切换选项  Esc 取消  Ctrl+C 拒绝"))
	b.WriteString("\n")
	if a.Title != "" {
		b.WriteString("  " + a.Title + "\n")
	}
	for _, line := range strings.Split(a.Message, "\n") {
		b.WriteString(styFaint.Render("  "+line) + "\n")
	}
	for i, f := range a.Fields {
		mark := "  "
		if i == st.cursor {
			mark = "> "
		}
		label := fieldTitle(f)
		if f.Required {
			label += "（必填）"
		}
		b.WriteString(mark + label + "  " + m.askFieldView(st, i) + "\n")
		if f.Desc != "" {
			b.WriteString(styFaint.Render("    "+f.Desc) + "\n")
		}
	}
	submit := "提交"
	if len(a.Fields) == 0 {
		submit = "允许"
	}
	if st.cursor == len(a.Fields) {
		submit = styEffortSel.Render(" " + submit + " ")
	} else {
		submit = styFaint.Render("[" + submit + "]")
	}
	b.WriteString("  " + submit)
	if st.err != "" {
		b.WriteString("\n" + styErr.Render("  "+st.err))
	}
	lines := strings.Split(b.String(), "\n")
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, w, "…")
	}
	return strings.Join(lines, "\n")
}

// askFieldView 渲染单个字段的当前值。
func (m *Model) askFieldView(st *askState, i int) string {
	f := st.ask.Fields[i]
	switch f.Kind {
	case "boolean":
		yes, no := "是", "否"
		if st.picks[i] == 1 {
			yes = styEffortSel.Render(" " + yes + " ")
		} else if st.picks[i] == 0 {
			no = styEffortSel.Render(" " + no + " ")
		}
		return no + " / " + yes
	case "enum":
		var parts []string
		for j, c := range f.Choices {
			label := c
			if j < len(f.Labels) && f.Labels[j] != "" {
				label = f.Labels[j]
			}
			if j == st.picks[i] {
				label = styEffortSel.Render(" " + label + " ")
			} else {
				label = styFaint.Render(label)
			}
			parts = append(parts, label)
		}
		return strings.Join(parts, "  ")
	}
	v := st.bufs[i]
	if i == st.cursor {
		v += "▍"
	}
	if v == "" {
		return styFaint.Render("（未填）")
	}
	return v
}
