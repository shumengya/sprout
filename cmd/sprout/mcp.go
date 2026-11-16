package main

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/config"
	"github.com/shumengya/sprout/internal/agent/mcp"
	"github.com/shumengya/sprout/internal/agent/tool"
	"github.com/shumengya/sprout/internal/tui"
)

// loadMCPConfig 读取 mcp.jsonc。缺失或为空时先补一份注释模板（全部注释掉，不启用任何服务端）。
//
// 模板里全部是注释，不启用任何服务端；已有内容时原样保留。
func loadMCPConfig(dataDir string) (*config.MCPFile, error) {
	path := config.MCPPath(dataDir)
	if _, err := config.EnsureMCPTemplate(path); err != nil {
		return nil, err
	}
	return config.LoadMCP(path)
}

// mcpConnectTimeout 是启动时等待所有 MCP 服务端就绪的总上限。
// 单个服务端还会按自己的 timeoutSeconds 超时，这里是最后一道闸：
// 有一个服务端卡住不该让整个 sprout 起不来。
const mcpConnectTimeout = 20 * time.Second

// mcpSet 是 MCP 服务端的运行状态：连上的客户端 + 全部服务端的展示信息。
//
// servers 里包含未启用的服务端（面板要把它们列出来供用户打开），clients 只收连上的。
type mcpSet struct {
	clients map[string]*mcp.Client
	servers []tui.MCPServerInfo
}

// newMCPSet 按配置生成服务端列表，全部标记为未连接。
func newMCPSet(f *config.MCPFile) *mcpSet {
	set := &mcpSet{clients: map[string]*mcp.Client{}}
	for _, name := range f.ServerNames() {
		s := f.Servers[name]
		info := tui.MCPServerInfo{Name: name, URL: s.URL, Command: s.Command, Enabled: s.Enable}
		if s.Enable {
			info.Pending = true
		}
		set.servers = append(set.servers, info)
	}
	return set
}

// clone 复制一份集合。面板操作在后台 goroutine 里改状态，TUI 线程同时在读，
// 所以改动都落在副本上，最后整体替换，不原地改正在被读的切片。
func (s *mcpSet) clone() *mcpSet {
	out := &mcpSet{clients: make(map[string]*mcp.Client, len(s.clients))}
	for name, c := range s.clients {
		out.clients[name] = c
	}
	out.servers = append([]tui.MCPServerInfo(nil), s.servers...)
	return out
}

// find 返回某个服务端在 servers 里的下标。
func (s *mcpSet) find(name string) int {
	for i, srv := range s.servers {
		if srv.Name == name {
			return i
		}
	}
	return -1
}

// startMCP 在后台连接所有启用的服务端。连好后由 applyMCP 把工具接进 agent。
//
// 服务端在网络那头，握手加列工具要好几秒；同步等待会让界面迟迟不出现。
// 所以启动时先把“连接中”写进 info.MCP，界面立刻可用；连好后再注册工具、重建 agent。
// 这期间没有 MCP 工具也只是少了几个工具，内置工具照常工作。
func (a *app) startMCP(f *config.MCPFile) {
	set := newMCPSet(f)
	a.info.MCP = set.servers

	a.mcpVersions = mcp.LoadVersions(config.MCPVersionsPath(a.dataDir))
	a.mcpReady = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	a.mcpCancel = cancel
	go func() {
		defer close(a.mcpReady)
		connectMCP(ctx, set, f, a.env, a.log, a.mcpVersions, a.specTweak())
		a.saveMCPTools()
		if err := a.mcpVersions.Save(); err != nil {
			a.log.Debug("写入 MCP 版本缓存失败", "error", err)
		}
		a.mu.Lock()
		a.mcp = set
		a.mu.Unlock()
	}()
}

// waitMCP 等后台连接结束。没有启用服务端时立即返回。
func (a *app) waitMCP() {
	if a.mcpReady != nil {
		<-a.mcpReady
	}
}

// mcpSnapshot 一次取出当前的服务端集合与配置。两者必须来自同一时刻，
// 否则面板改了配置、后台还按旧配置注册工具时会漏掉或多出工具。
func (a *app) mcpSnapshot() (*mcpSet, *config.MCPFile) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mcp, a.mcpCfg
}

// applyMCP 把已连上的 MCP 工具注册进当前 agent，并保留对话历史，返回新的 info 与要告知用户的提示。
//
// agent 构造时就把工具 schema 和系统提示词烘进了请求，所以增删工具、改技能开关都必须重建 agent；
// 历史与摘要原样搬过去。调用方必须保证此时没有 Run 在进行（TUI 空闲时才调用，
// run 模式在第一次提问前调用），否则重建出来的 agent 会漏掉进行中那一轮的消息。
func (a *app) applyMCP() (tui.Info, []string) {
	// 技能开关改了也要走到重建这一步，所以不能因为没配置 MCP 就提前返回。
	a.mu.Lock()
	dirty := a.skillsDirty
	a.mu.Unlock()
	set, _ := a.mcpSnapshot()
	if set == nil && !dirty {
		return a.info, nil
	}

	var notices []string
	if set != nil {
		a.info.MCP = append([]tui.MCPServerInfo(nil), set.servers...)
		for _, s := range a.info.MCP {
			if s.Err != "" {
				notices = append(notices, fmt.Sprintf("MCP 服务端 %s 连接失败：%s", s.Name, s.Err))
			}
		}
	}
	a.info.Skills = a.skillInfosLocked()
	// 没有可用 agent（缺 Key 等）时只刷新展示；registerTools 会改 info.Tools，无 agent 时不必动它。
	if a.ag == nil || a.rec == nil {
		return a.info, notices
	}

	// registerTools 会重写 info.Tools / Unavailable；失败时退回原样，不留半截状态。
	tools, unavailable := a.info.Tools, a.info.Unavailable
	reg, err := a.registerTools(a.cfg, a.env)
	if err == nil {
		var ag *agent.Agent
		if ag, err = a.buildAgent(a.sel, a.key, reg, a.rec, a.env, a.log); err == nil {
			ag.Restore(a.ag.History(), a.ag.Summary())
			a.ag, a.reg = ag, reg
			a.mu.Lock()
			a.skillsDirty = false
			a.mu.Unlock()
			a.log.Info("agent 已重建", "tools", len(reg.Names()), "skills", len(a.info.Skills))
			return a.info, notices
		}
	}
	a.info.Tools, a.info.Unavailable = tools, unavailable
	return a.info, append(notices, "MCP 工具接入失败："+err.Error())
}

// Close 断开全部服务端。
func (s *mcpSet) Close() {
	if s == nil {
		return
	}
	for _, c := range s.clients {
		// 关闭失败不影响退出：子进程已被终止，最坏是服务端那边留个空会话。
		_ = c.Close()
	}
}

// connectMCP 并行连接所有启用的服务端，把结果填进 set。
//
// 连接失败不阻止启动：MCP 是扩展能力，某个服务端没起来时 agent 照样能用内置工具，
// 失败原因写进 MCPServerInfo.Err 交给 /tools 与 /mcp 展示。
func connectMCP(ctx context.Context, set *mcpSet, f *config.MCPFile, e env, log *slog.Logger, versions *mcp.Versions, tweak ...func(*mcp.Spec)) {
	names := f.EnabledServers()
	if len(names) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, mcpConnectTimeout)
	defer cancel()

	type result struct {
		name   string
		spec   mcp.Spec
		client *mcp.Client
		err    error
	}
	ch := make(chan result, len(names))
	for _, name := range names {
		spec := mcpSpec(name, f.Servers[name], e.getenv)
		// 上次谈成的版本直接用来握手，跳过一轮注定失败的 server/discover 试探。
		spec.KnownVersion = versions.Lookup(spec)
		for _, fn := range tweak {
			fn(&spec)
		}
		go func() {
			c, err := mcp.Dial(ctx, spec, e.http, log)
			ch <- result{name: name, spec: spec, client: c, err: err}
		}()
	}

	for range names {
		r := <-ch
		i := set.find(r.name)
		if i < 0 {
			continue
		}
		if r.err != nil {
			set.servers[i].Pending, set.servers[i].Err = false, r.err.Error()
			continue
		}
		set.clients[r.name] = r.client
		fillServerInfo(&set.servers[i], r.client, f.Servers[r.name])
		versions.Record(r.spec, r.client.Version())
	}
}

// fillServerInfo 把连上的客户端信息写进展示结构。
func fillServerInfo(info *tui.MCPServerInfo, c *mcp.Client, srv config.MCPServer) {
	info.Pending, info.Err = false, ""
	info.Version = c.Version()
	info.Server = c.ServerInfo().Name
	info.Tools = mcpToolInfos(c.Tools(), srv)
}

// mcpToolInfos 把服务端工具转成展示结构，按 excludeTools 标出哪些不交给模型。
func mcpToolInfos(tools []mcp.ToolInfo, srv config.MCPServer) []tui.MCPToolInfo {
	out := make([]tui.MCPToolInfo, 0, len(tools))
	for _, t := range tools {
		out = append(out, tui.MCPToolInfo{Name: t.Name, Desc: t.Description, Enabled: !srv.IsExcluded(t.Name)})
	}
	return out
}

// mcpSpec 把配置转成客户端需要的 Spec，并展开 env / headers 里的 ${环境变量}。
func mcpSpec(name string, s config.MCPServer, getenv func(string) string) mcp.Spec {
	spec := mcp.Spec{
		Name:    name,
		URL:     expandEnv(s.URL, getenv),
		Command: expandEnv(s.Command, getenv),
		Dir:     s.Cwd,
	}
	for _, a := range s.Args {
		spec.Args = append(spec.Args, expandEnv(a, getenv))
	}
	if len(s.Env) > 0 {
		spec.Env = make(map[string]string, len(s.Env))
		for k, v := range s.Env {
			spec.Env[k] = expandEnv(v, getenv)
		}
	}
	if len(s.Headers) > 0 {
		spec.Headers = make(map[string]string, len(s.Headers))
		for k, v := range s.Headers {
			spec.Headers[k] = expandEnv(v, getenv)
		}
	}
	if s.TimeoutSeconds > 0 {
		spec.Timeout = time.Duration(s.TimeoutSeconds) * time.Second
	}
	return spec
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnv 把 ${NAME} 换成当前进程的环境变量；没设置的变量替换成空串。
func expandEnv(s string, getenv func(string) string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	return envRef.ReplaceAllStringFunc(s, func(m string) string {
		return getenv(m[2 : len(m)-1])
	})
}

// registerMCPTools 把 MCP 工具注册进注册表。
//
// 名字统一是 mcp__<服务端>__<工具>。同名时先注册的赢：内置工具 > 外部工具 > MCP，
// 冲突的那条记进 unavailable，让用户知道为什么少了一个工具。
func registerMCPTools(reg *tool.Registry, set *mcpSet, f *config.MCPFile, add func(tool.Tool, string) error, log *slog.Logger) []string {
	if set == nil || f == nil {
		return nil
	}
	var notes []string
	// 按服务端名排序注册，保证每次启动的工具顺序一致，冲突时的胜负也稳定。
	for _, name := range f.ServerNames() {
		c := set.clients[name]
		if c == nil {
			continue
		}
		srv := f.Servers[name]
		for _, info := range c.Tools() {
			if srv.IsExcluded(info.Name) {
				continue
			}
			full := mcp.ToolName(name, info.Name)
			if _, exists := reg.Get(full); exists {
				notes = append(notes, "MCP 工具 "+full+" 与已注册的工具重名，已跳过")
				continue
			}
			if err := add(mcp.NewTool(c, full, info.Name, info), "MCP  "+name+"  "+info.Name); err != nil {
				notes = append(notes, err.Error())
				continue
			}
			log.Info("已注册 MCP 工具", "server", name, "tool", info.Name, "name", full)
		}
		// 资源工具不受 excludeTools 控制：服务端的工具列表里没有这两个名字。
		for _, rt := range mcp.ResourceTools(c) {
			if _, exists := reg.Get(rt.Name()); exists {
				notes = append(notes, "MCP 工具 "+rt.Name()+" 与已注册的工具重名，已跳过")
				continue
			}
			if err := add(rt, "MCP  "+name+"  资源"); err != nil {
				notes = append(notes, err.Error())
			}
		}
	}
	return notes
}

// mcpStore 把 app 适配成 tui.MCPPanel。
//
// 操作都在 tea.Cmd 的 goroutine 里跑：写配置、重连都可能阻塞。返回后由 TUI 在空闲时
// 调 applyMCP 重建注册表与 agent，原因见 applyMCP 的注释。
type mcpStore struct{ a *app }

var _ tui.MCPPanel = mcpStore{}

func (s mcpStore) Servers() []tui.MCPServerInfo {
	set, _ := s.a.mcpSnapshot()
	if set == nil {
		return nil
	}
	return append([]tui.MCPServerInfo(nil), set.servers...)
}

// SetServer 启用或停用一个服务端：写回 mcp.jsonc，再连上或断开它。
func (s mcpStore) SetServer(name string, enable bool) (string, error) {
	a := s.a
	if !a.sel.Model.SupportsTools() {
		return "", fmt.Errorf("当前模型不支持工具调用，MCP 服务端用不上")
	}
	a.waitMCP()
	if err := config.SetMCPServerEnabled(config.MCPPath(a.dataDir), name, enable); err != nil {
		return "", err
	}
	f, err := config.LoadMCP(config.MCPPath(a.dataDir))
	if err != nil {
		return "", err
	}

	a.mu.Lock()
	base := a.mcp
	a.mcpCfg = f
	a.mu.Unlock()
	if base == nil {
		// 启动时一个服务端都没启用（或没配置），集合还没建过：现在补一个。
		base = newMCPSet(f)
	}
	set := base.clone()
	i := set.find(name)
	if i < 0 {
		return "", fmt.Errorf("mcp.jsonc 里没有服务端 %q", name)
	}

	srv := f.Servers[name]
	if !enable {
		if c := set.clients[name]; c != nil {
			_ = c.Close()
			delete(set.clients, name)
		}
		set.servers[i] = tui.MCPServerInfo{Name: name, URL: srv.URL, Command: srv.Command, Enabled: false}
		a.commitMCPSet(set)
		return "已停用服务端 " + name + "，它的工具不再提供给模型", nil
	}

	// 先让界面看到“连接中”，连上（或失败）后再换上最终状态。
	set.servers[i].Pending, set.servers[i].Err = true, ""
	a.commitMCPSet(set)

	ctx, cancel := context.WithTimeout(context.Background(), mcpConnectTimeout)
	defer cancel()
	versions := a.mcpVersionsLocked()
	spec := mcpSpec(name, srv, a.env.getenv)
	spec.KnownVersion = versions.Lookup(spec)
	a.specTweak()(&spec)
	c, err := mcp.Dial(ctx, spec, a.env.http, a.log)
	if err != nil {
		// 连不上也要把 enable 留在配置里：用户修好网络后按 r 重连即可，不用再点一次开关。
		set.servers[i].Pending, set.servers[i].Err = false, err.Error()
		a.commitMCPSet(set)
		return "", fmt.Errorf("已启用 %s，但连接失败：%w", name, err)
	}
	versions.Record(spec, c.Version())
	if err := versions.Save(); err != nil {
		a.log.Debug("写入 MCP 版本缓存失败", "error", err)
	}
	set.clients[name] = c
	set.servers[i].Enabled = true
	fillServerInfo(&set.servers[i], c, srv)
	a.commitMCPSet(set)
	return fmt.Sprintf("已启用服务端 %s（%d 个工具）", name, set.servers[i].ToolCount()), nil
}

// mcpVersionsLocked 取版本缓存，没有就建一个。面板操作与启动流程可能先后用到它。
func (a *app) mcpVersionsLocked() *mcp.Versions {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.mcpVersions == nil {
		a.mcpVersions = mcp.LoadVersions(config.MCPVersionsPath(a.dataDir))
	}
	return a.mcpVersions
}

// SetTool 启用或停用一个工具：写进服务端的 excludeTools，不重连。
func (s mcpStore) SetTool(server, toolName string, enable bool) (string, error) {
	a := s.a
	a.waitMCP()
	if err := config.SetMCPToolExcluded(config.MCPPath(a.dataDir), server, toolName, !enable); err != nil {
		return "", err
	}
	f, err := config.LoadMCP(config.MCPPath(a.dataDir))
	if err != nil {
		return "", err
	}

	a.mu.Lock()
	base := a.mcp
	a.mcpCfg = f
	a.mu.Unlock()
	if base == nil {
		return "", fmt.Errorf("服务端 %s 还没配置好", server)
	}
	set := base.clone()
	i := set.find(server)
	if i < 0 {
		return "", fmt.Errorf("mcp.jsonc 里没有服务端 %q", server)
	}
	// 工具列表来自连接时的快照，重新按 excludeTools 标一遍即可，不必重连。
	set.servers[i].Tools = mcpToolInfos(clientTools(set.clients[server]), f.Servers[server])
	a.commitMCPSet(set)

	action := "启用"
	if !enable {
		action = "停用"
	}
	return fmt.Sprintf("已%s工具 %s / %s", action, server, toolName), nil
}

// clientTools 返回客户端发现的工具；客户端为空时返回 nil。
func clientTools(c *mcp.Client) []mcp.ToolInfo {
	if c == nil {
		return nil
	}
	return c.Tools()
}

// Refresh 重读 mcp.jsonc，断开并重连全部已启用的服务端。
//
// 重新读盘是有意的：用户可能在编辑器里改过地址、命令或开关，
// 只重连旧配置会让这些改动看起来没生效。
func (s mcpStore) Refresh() (string, error) {
	a := s.a
	a.waitMCP()
	f, err := config.LoadMCP(config.MCPPath(a.dataDir))
	if err != nil {
		return "", err
	}

	a.mu.Lock()
	old := a.mcp
	a.mcpCfg = f
	a.mu.Unlock()
	old.Close()

	set := newMCPSet(f)
	versions := a.mcpVersionsLocked()
	// 用户主动刷新就是要看最新的工具：先清掉缓存，别又读回旧列表。
	for _, name := range f.ServerNames() {
		a.mcpTools.Drop(name)
	}
	connectMCP(context.Background(), set, f, a.env, a.log, versions, a.specTweak())
	a.saveMCPTools()
	if err := versions.Save(); err != nil {
		a.log.Debug("写入 MCP 版本缓存失败", "error", err)
	}
	a.commitMCPSet(set)

	ok := 0
	var failed []string
	for _, srv := range set.servers {
		switch {
		case !srv.Enabled:
		case srv.Err != "":
			failed = append(failed, srv.Name)
		default:
			ok++
		}
	}
	notice := fmt.Sprintf("已刷新 MCP 服务端：%d 个已连接", ok)
	if len(failed) > 0 {
		notice += "，" + strings.Join(failed, "、") + " 连接失败"
	}
	return notice, nil
}

// commitMCPSet 在锁内换上新的服务端集合。
func (a *app) commitMCPSet(set *mcpSet) {
	a.mu.Lock()
	a.mcp = set
	a.mu.Unlock()
}
