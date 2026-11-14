package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// MCPToolInfo 是 MCP 服务端上的一个工具，供 /mcp 面板展示与开关。
type MCPToolInfo struct {
	Name    string // 服务端上的原始工具名
	Desc    string // 服务端给的说明，面板里截断显示
	Enabled bool   // 是否交给模型
}

// MCPPanel 让 TUI 读取并修改 MCP 服务端与工具的开关，由 main 提供实现。
//
// 写回 mcp.jsonc 与改连接在实现里完成；工具注册表与 agent 的重建不在这里做——
// 那一步只能在 TUI 空闲时的主线程上跑（见 Model.applyMCP），所以操作完成后
// TUI 会自己触发一次 applyMCP，面板的列表也从刷新后的 Info 取。
type MCPPanel interface {
	// Servers 返回全部已配置的服务端（含未启用的）。未连接的服务端没有工具列表。
	Servers() []MCPServerInfo
	// SetServer 启用或停用一个服务端，返回给用户看的提示。启用要连接服务端，可能耗时。
	SetServer(name string, enable bool) (string, error)
	// SetTool 启用或停用一个工具：写进 excludeTools，不重连。
	SetTool(server, tool string, enable bool) (string, error)
	// Refresh 重读 mcp.jsonc，断开并重连全部已启用的服务端。
	Refresh() (string, error)
}

// mcpRow 是面板里的一行：服务端，或者展开后它下面的某个工具。
type mcpRow struct {
	server string
	tool   string // 非空表示这是工具行
}

// mcpPicker 是 /mcp 的服务端面板：替换输入框，↑↓ 选择，空格开关，→ 展开，r 刷新。
type mcpPicker struct {
	servers  []MCPServerInfo
	expanded map[string]bool
	cursor   int
	offset   int
	busy     string // 非空表示正在执行某个操作（重连可能要几秒），期间不再接受新操作
	note     string // 最近一次操作的结果
	noteOK   bool
}

// rows 展开成可上下移动的行列表：服务端行，后面跟它展开时的工具行。
func (p *mcpPicker) rows() []mcpRow {
	var out []mcpRow
	for _, s := range p.servers {
		out = append(out, mcpRow{server: s.Name})
		if !p.expanded[s.Name] {
			continue
		}
		for _, t := range s.Tools {
			out = append(out, mcpRow{server: s.Name, tool: t.Name})
		}
	}
	return out
}

// findServer 按名字取服务端。
func (p *mcpPicker) findServer(name string) (MCPServerInfo, bool) {
	for _, s := range p.servers {
		if s.Name == name {
			return s, true
		}
	}
	return MCPServerInfo{}, false
}

// selected 返回当前高亮的行；列表为空时 ok 为 false。
func (p *mcpPicker) selected() (mcpRow, bool) {
	rows := p.rows()
	if len(rows) == 0 {
		return mcpRow{}, false
	}
	return rows[min(p.cursor, len(rows)-1)], true
}

// move 上下移动光标，不越界。
func (p *mcpPicker) move(d int) {
	n := len(p.rows())
	p.cursor = min(max(p.cursor+d, 0), max(n-1, 0))
}

// mcpCmd 处理 /mcp。没有实现时说明为什么不能用。
func (m *Model) mcpCmd() {
	s := m.opts.MCP
	switch {
	case s == nil:
		m.add(&entry{kind: kindNotice, text: "当前不能管理 MCP 服务端（没有可用的 agent，或当前模型不支持工具）。"})
	case m.busy():
		m.status = "正在生成、恢复或压缩中，暂时不能管理 MCP"
	default:
		p := &mcpPicker{expanded: map[string]bool{}}
		for _, s := range s.Servers() {
			p.expanded[s.Name] = true // 默认展开：打开面板就能看到工具
		}
		p.servers = s.Servers()
		m.mcpPicker = p
		m.status = ""
	}
}

// mcpDoneMsg 是面板操作的完成消息。
type mcpDoneMsg struct {
	notice string
	err    error
}

// mcpKey 在面板打开时处理按键，返回 true 表示已消费。
func (m *Model) mcpKey(k tea.KeyPressMsg) (bool, tea.Cmd) {
	p := m.mcpPicker
	if p == nil {
		return false, nil
	}
	key := k.String()
	if p.busy != "" { // 操作进行中：只允许 Esc 之类的按键关闭，其余忽略，避免并发改同一份配置
		if key == "esc" || key == "ctrl+c" {
			p.busy = ""
			m.status = "MCP 操作还在进行，等它结束再关闭面板"
		}
		return true, nil
	}
	row, hasRow := p.selected()
	switch key {
	case "esc", "ctrl+c":
		m.mcpPicker = nil
		m.status = "已关闭 MCP 面板"
	case "up", "ctrl+p", "k":
		p.cursor--
	case "down", "ctrl+n", "j":
		p.cursor++
	case "pgup":
		p.move(-m.mcpRows())
	case "pgdown":
		p.move(m.mcpRows())
	case "home":
		p.cursor = 0
	case "end":
		p.cursor = 1 << 30
	case "right", "l":
		if hasRow && row.tool == "" {
			p.expanded[row.server] = true
			p.syncCursor()
		}
	case "left", "h":
		if hasRow {
			if row.tool != "" {
				p.expanded[row.server] = false
				p.syncCursor()
				// 收起后光标要落在原来的服务端行上。
				if i := p.indexOf(mcpRow{server: row.server}); i >= 0 {
					p.cursor = i
				}
			} else {
				p.expanded[row.server] = false
			}
			p.syncCursor()
		}
	case "space", "enter": // bubbletea v2 里空格的 String() 是 "space"
		if !hasRow {
			return true, nil
		}
		return true, m.mcpToggle(row)
	case "r":
		return true, m.mcpDo("正在重新连接 MCP 服务端…", func(s MCPPanel) (string, error) { return s.Refresh() })
	}
	p.syncCursor()
	return true, nil
}

// indexOf 返回某行在展开列表里的下标。
func (p *mcpPicker) indexOf(want mcpRow) int {
	for i, r := range p.rows() {
		if r == want {
			return i
		}
	}
	return -1
}

// syncCursor 让光标落在有效范围内。
func (p *mcpPicker) syncCursor() {
	p.cursor = min(max(p.cursor, 0), max(len(p.rows())-1, 0))
}

// mcpToggle 开关当前行：服务端切换 enable，工具切换 excludeTools。
func (m *Model) mcpToggle(row mcpRow) tea.Cmd {
	srv, ok := m.mcpPicker.findServer(row.server)
	if !ok {
		return nil
	}
	if row.tool == "" {
		enable := !srv.Enabled
		label := "启用"
		if !enable {
			label = "停用"
		}
		return m.mcpDo(fmt.Sprintf("正在%s服务端 %s…", label, row.server), func(p MCPPanel) (string, error) {
			return p.SetServer(row.server, enable)
		})
	}
	enable := true
	label := "启用"
	for _, t := range srv.Tools {
		if t.Name == row.tool && t.Enabled {
			enable, label = false, "停用"
		}
	}
	return m.mcpDo(fmt.Sprintf("正在%s工具 %s / %s…", label, row.server, row.tool), func(p MCPPanel) (string, error) {
		return p.SetTool(row.server, row.tool, enable)
	})
}

// mcpDo 在 tea.Cmd 里执行一个面板操作。写配置与重连都可能阻塞，不能放进 Update。
func (m *Model) mcpDo(status string, op func(MCPPanel) (string, error)) tea.Cmd {
	s := m.opts.MCP
	m.mcpPicker.busy = status
	m.status = status
	return func() tea.Msg {
		notice, err := op(s)
		return mcpDoneMsg{notice: notice, err: err}
	}
}

// onMCPDone 处理面板操作结果。成功时让 Update 的收尾去跑 applyMCP：
// 工具注册表与 agent 的重建只能在主线程、空闲时做。
func (m *Model) onMCPDone(msg mcpDoneMsg) {
	if msg.err == nil {
		m.mcpDone = m.opts.ApplyMCP != nil
	}
	p := m.mcpPicker
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

// syncMCPServers 把刷新后的服务端列表交给打开着的面板。
func (m *Model) syncMCPServers() {
	if m.mcpPicker == nil {
		return
	}
	m.mcpPicker.servers = m.opts.Info.MCP
	m.mcpPicker.syncCursor()
}

// mcpRows 是面板列表最多显示的行数：屏幕越高显示越多，3 到 14 行。
func (m *Model) mcpRows() int { return min(max(m.h-14, 3), 14) }

// mcpView 渲染 MCP 面板，替换输入框的位置。
func (m *Model) mcpView(w int) string {
	p := m.mcpPicker
	var lines []string

	lines = append(lines, spread(
		styBrowseTitle.Render("MCP 服务端"),
		styFaint.Render(fmt.Sprintf("已启用 %d/%d", p.enabledCount(), len(p.servers))), w))
	lines = append(lines, styFaint.Render("↑↓ 选择 · 空格 / Enter 开关 · → 展开 · ← 收起 · r 刷新 · Esc 关闭"))

	if len(p.servers) == 0 {
		lines = append(lines, styFaint.Render("  没有配置 MCP 服务端，在数据目录的 mcp.jsonc 里添加。"))
	}
	rows := p.rows()
	if len(rows) > 0 {
		p.cursor = min(p.cursor, len(rows)-1)
		height := m.mcpRows()
		if p.cursor < p.offset {
			p.offset = p.cursor
		}
		if p.cursor >= p.offset+height {
			p.offset = p.cursor - height + 1
		}
		p.offset = max(min(p.offset, len(rows)-height), 0)
		for i := p.offset; i < min(p.offset+height, len(rows)); i++ {
			lines = append(lines, p.row(m, rows[i], i == p.cursor, w))
		}
	}

	foot := ""
	if len(rows) > 0 {
		foot = styFaint.Render(fmt.Sprintf("(%d/%d)", p.cursor+1, len(rows)))
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

// enabledCount 数已启用的服务端。
func (p *mcpPicker) enabledCount() int {
	n := 0
	for _, s := range p.servers {
		if s.Enabled {
			n++
		}
	}
	return n
}

// row 渲染一行：服务端行带连接状态与工具数，工具行缩进一级。
func (p *mcpPicker) row(m *Model, r mcpRow, sel bool, w int) string {
	srv, _ := p.findServer(r.server)
	if r.tool != "" {
		var t MCPToolInfo
		for _, it := range srv.Tools {
			if it.Name == r.tool {
				t = it
			}
		}
		mark := styFaint.Render("○")
		if t.Enabled {
			mark = styOK.Render("●")
		}
		text := "    " + t.Name
		if t.Desc != "" {
			text += "  " + firstLine(t.Desc)
		}
		if sel {
			return ansi.Truncate(styReverse.Render("  ❯ "+mark+" "+text), w, "…")
		}
		return ansi.Truncate("  "+mark+" "+text, w, "…")
	}

	var b strings.Builder
	mark := styFaint.Render("−") // 未启用
	switch {
	case !srv.Enabled:
	case srv.Pending:
		mark = styFaint.Render("◌")
	case srv.Err != "":
		mark = styErr.Render("✗")
	default:
		mark = styOK.Render("●")
	}
	arrow := "▸"
	if p.expanded[srv.Name] {
		arrow = "▾"
	}
	b.WriteString(arrow + " " + mark + " " + srv.Name)

	var meta string
	switch {
	case !srv.Enabled:
		meta = "已停用"
	case srv.Pending:
		meta = "连接中…"
	case srv.Err != "":
		meta = "连接失败"
	default:
		meta = fmt.Sprintf("%d/%d 个工具", enabledTools(srv), len(srv.Tools))
	}
	if sel {
		line := "❯ " + arrow + " " + mark + " " + srv.Name
		line = spread2(line, styFaint.Render(meta), w)
		return ansi.Truncate(styReverse.Render(line), w, "…")
	}
	b.WriteString(styFaint.Render("  " + meta))
	return ansi.Truncate(b.String(), w, "…")
}

func enabledTools(s MCPServerInfo) int { return s.ToolCount() }

// spread2 与 spread 相同，但放不下时只留 left——选中行整体反色，右侧信息不单独上色。
func spread2(left, right string, w int) string {
	gap := w - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap < 2 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right
}
