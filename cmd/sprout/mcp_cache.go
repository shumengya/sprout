package main

// saveMCPTools 把工具缓存写盘；写不进去只记调试日志，缓存不是权威，不影响使用。
func (a *app) saveMCPTools() {
	if err := a.mcpTools.Save(); err != nil {
		a.log.Debug("写入 MCP 工具缓存失败", "error", err)
	}
}

// onMCPToolsChanged 在服务端发来 tools/list_changed 并且客户端已重新取回列表后调用，跑在后台 goroutine。
//
// 这里只更新展示用的工具列表，并把 skillsDirty 当作“待重建 agent”的标记：工具 schema 烘在请求里，
// 必须等空闲时由 TUI 调 applyMCP 重建，不能在生成中途换掉。
func (a *app) onMCPToolsChanged(server string, _ int) {
	a.mu.Lock()
	base, f := a.mcp, a.mcpCfg
	if base == nil || f == nil {
		a.mu.Unlock()
		return
	}
	set := base.clone()
	i, c := set.find(server), set.clients[server]
	if i < 0 || c == nil {
		a.mu.Unlock()
		return
	}
	set.servers[i].Tools = mcpToolInfos(c.Tools(), f.Servers[server])
	a.mcp = set
	a.skillsDirty = true
	a.mu.Unlock()

	a.saveMCPTools()
	// 通道容量为 1：已经有一条待处理的通知时不再堆积，TUI 处理一次就会把最新状态全部接上。
	select {
	case a.mcpChanged <- struct{}{}:
	default:
	}
}
