package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// fakeMCP 记录面板发来的操作，返回固定的服务端列表。
type fakeMCP struct {
	servers []MCPServerInfo
	calls   []string
	err     error
	notice  string
}

func (f *fakeMCP) Servers() []MCPServerInfo { return f.servers }

func (f *fakeMCP) SetServer(name string, enable bool) (string, error) {
	f.calls = append(f.calls, "server:"+name+":"+boolTag(enable))
	return f.notice, f.err
}

func (f *fakeMCP) SetTool(server, tool string, enable bool) (string, error) {
	f.calls = append(f.calls, "tool:"+server+"/"+tool+":"+boolTag(enable))
	return f.notice, f.err
}

func (f *fakeMCP) Refresh() (string, error) {
	f.calls = append(f.calls, "refresh")
	return f.notice, f.err
}

func boolTag(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func mcpServers() []MCPServerInfo {
	return []MCPServerInfo{
		{
			Name: "github", URL: "https://x/mcp", Enabled: true, Version: "2025-06-18",
			Tools: []MCPToolInfo{
				{Name: "search", Desc: "搜索仓库", Enabled: true},
				{Name: "write_file", Desc: "写文件", Enabled: false},
			},
		},
		{Name: "fs", Command: "npx", Enabled: false},
	}
}

// openMCP 打开 /mcp 面板。
func openMCP(t *testing.T, f *fakeMCP) *Model {
	t.Helper()
	m := newSized(t, Options{Runner: scriptRunner(), Info: Info{MCP: f.Servers()}, MCP: f}, 120, 40)
	m.in.SetValue("/mcp")
	m.Update(keyEnter)
	if m.mcpPicker == nil {
		t.Fatal("/mcp 应打开面板")
	}
	return m
}

func TestMCPPanelListsServersAndTools(t *testing.T) {
	m := openMCP(t, &fakeMCP{servers: mcpServers()})
	screen := plain(m)
	mustContain(t, screen, "MCP 服务端", "已启用 1/2", "github", "search", "write_file", "fs",
		"已停用", "1/2 个工具", "空格 / Enter 开关", "r 刷新", "Esc 关闭")
}

func TestMCPPanelTogglesServer(t *testing.T) {
	f := &fakeMCP{servers: mcpServers(), notice: "已停用服务端 github"}
	m := openMCP(t, f)
	// 光标在 github 上，空格停用。
	_, cmd := m.Update(typed(" "))
	if m.mcpPicker.busy == "" {
		t.Error("操作期间面板应标记为忙")
	}
	runCmd(m, cmd)
	if len(f.calls) != 1 || f.calls[0] != "server:github:off" {
		t.Fatalf("应请求停用 github，得到 %v", f.calls)
	}
	if m.mcpPicker.busy != "" || m.mcpPicker.note != "已停用服务端 github" {
		t.Errorf("完成后应显示结果: busy=%q note=%q", m.mcpPicker.busy, m.mcpPicker.note)
	}
}

func TestMCPPanelTogglesFirstServerOn(t *testing.T) {
	servers := mcpServers()
	servers[1].Enabled = true
	f := &fakeMCP{servers: servers}
	m := openMCP(t, f)
	m.mcpPicker.cursor = m.mcpPicker.indexOf(mcpRow{server: "github"})
	_, cmd := m.Update(typed(" "))
	runCmd(m, cmd)
	if len(f.calls) == 0 || f.calls[0] != "server:github:off" {
		t.Errorf("启用的服务端应被停用，得到 %v", f.calls)
	}
}

func TestMCPPanelTogglesTool(t *testing.T) {
	f := &fakeMCP{servers: mcpServers()}
	m := openMCP(t, f)
	// 往下两行到 write_file 工具行，它是停用的，空格应把它打开。
	m.Update(press(tea.KeyDown, 0))
	m.Update(press(tea.KeyDown, 0))
	if row, _ := m.mcpPicker.selected(); row.tool != "write_file" {
		t.Fatalf("光标应在 write_file 上，实际 %+v", row)
	}
	_, cmd := m.Update(keyEnter)
	runCmd(m, cmd)
	if len(f.calls) != 1 || f.calls[0] != "tool:github/write_file:on" {
		t.Errorf("应启用 write_file，得到 %v", f.calls)
	}
}

func TestMCPPanelCollapseSkipsTools(t *testing.T) {
	m := openMCP(t, &fakeMCP{servers: mcpServers()})
	rows := len(m.mcpPicker.rows())
	m.Update(typed("h")) // 收起 github
	if got := len(m.mcpPicker.rows()); got != rows-2 {
		t.Errorf("收起后应少两行工具，得到 %d", got)
	}
	m.Update(typed("l")) // 再展开
	if got := len(m.mcpPicker.rows()); got != rows {
		t.Errorf("展开后行数应恢复，得到 %d", got)
	}
}

func TestMCPPanelRefresh(t *testing.T) {
	f := &fakeMCP{servers: mcpServers(), notice: "已刷新 MCP 服务端：1 个已连接"}
	m := openMCP(t, f)
	_, cmd := m.Update(typed("r"))
	runCmd(m, cmd)
	if len(f.calls) != 1 || f.calls[0] != "refresh" {
		t.Fatalf("r 应触发刷新，得到 %v", f.calls)
	}
	if !strings.Contains(plain(m), "已刷新 MCP 服务端") {
		t.Errorf("应显示刷新结果:\n%s", plain(m))
	}
}

func TestMCPPanelShowsFailure(t *testing.T) {
	f := &fakeMCP{servers: mcpServers(), err: errors.New("连接超时")}
	m := openMCP(t, f)
	_, cmd := m.Update(typed(" "))
	runCmd(m, cmd)
	if !strings.Contains(plain(m), "连接超时") {
		t.Errorf("失败原因应显示在面板里:\n%s", plain(m))
	}
}

func TestMCPPanelBusyIgnoresToggle(t *testing.T) {
	f := &fakeMCP{servers: mcpServers()}
	m := openMCP(t, f)
	m.mcpPicker.busy = "正在刷新…"
	if _, cmd := m.Update(typed(" ")); cmd != nil {
		t.Error("忙时不该再发操作")
	}
	if len(f.calls) != 0 {
		t.Errorf("忙时不该调用实现，得到 %v", f.calls)
	}
}

func TestMCPPanelEscCloses(t *testing.T) {
	m := openMCP(t, &fakeMCP{servers: mcpServers()})
	m.Update(keyEsc)
	if m.mcpPicker != nil {
		t.Error("Esc 应关闭面板")
	}
	if !strings.Contains(plain(m), "已关闭 MCP 面板") {
		t.Errorf("应有提示:\n%s", plain(m))
	}
}

func TestMCPPanelRefusesWhileRunning(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner(), Info: Info{MCP: mcpServers()}, MCP: &fakeMCP{}}, 120, 40)
	m.running = true
	m.in.SetValue("/mcp")
	m.Update(keyEnter)
	if m.mcpPicker != nil {
		t.Error("生成中不该打开面板")
	}
	mustContain(t, plain(m), "暂时不能管理 MCP")
}

func TestMCPPanelWithoutImplementation(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 120, 40)
	submit(m, "/mcp")
	if m.mcpPicker != nil {
		t.Error("没有实现时不该打开面板")
	}
	mustContain(t, plain(m), "当前不能管理 MCP 服务端")
}

func TestMCPPanelEmptyList(t *testing.T) {
	m := openMCP(t, &fakeMCP{})
	mustContain(t, plain(m), "没有配置 MCP 服务端")
	// 空列表里按键不应 panic。
	for _, k := range []tea.KeyPressMsg{press(tea.KeyDown, 0), typed(" "), typed("h"), typed("l")} {
		m.Update(k)
	}
}
