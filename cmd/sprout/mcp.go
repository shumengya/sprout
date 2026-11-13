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

// mcpSet 是已连接的 MCP 服务端集合。
type mcpSet struct {
	clients []*mcp.Client
	servers []tui.MCPServerInfo
}

// startMCP 在后台连接所有启用的服务端。连好后由 applyMCP 把工具接进 agent。
//
// 服务端在网络那头，握手加列工具要好几秒；同步等待会让界面迟迟不出现。
// 所以启动时先把“连接中”写进 info.MCP，界面立刻可用；连好后再注册工具、重建 agent。
// 这期间没有 MCP 工具也只是少了几个工具，内置工具照常工作。
func (a *app) startMCP(f *config.MCPFile) {
	names := f.EnabledServers()
	for _, name := range names {
		s := f.Servers[name]
		a.info.MCP = append(a.info.MCP, tui.MCPServerInfo{Name: name, URL: s.URL, Command: s.Command, Pending: true})
	}

	a.mcpVersions = mcp.LoadVersions(config.MCPVersionsPath(a.dataDir))
	a.mcpReady = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	a.mcpCancel = cancel
	go func() {
		defer close(a.mcpReady)
		set := connectMCP(ctx, f, a.env, a.log, a.mcpVersions)
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

// applyMCP 把已连上的 MCP 工具注册进当前 agent，并保留对话历史，返回新的 info 与要告知用户的提示。
//
// agent 构造时就把工具 schema 烘进了请求模板，所以增删工具必须重建 agent；历史与摘要原样搬过去。
// 调用方必须保证此时没有 Run 在进行（TUI 空闲时才调用，run 模式在第一次提问前调用），
// 否则重建出来的 agent 会漏掉进行中那一轮的消息。
func (a *app) applyMCP() (tui.Info, []string) {
	set := a.connectedMCP()
	if set == nil {
		return a.info, nil
	}

	var notices []string
	servers := make([]tui.MCPServerInfo, len(set.servers))
	copy(servers, set.servers)
	for _, s := range servers {
		if s.Err != "" {
			notices = append(notices, fmt.Sprintf("MCP 服务端 %s 连接失败：%s", s.Name, s.Err))
		}
	}
	a.info.MCP = servers
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
			a.log.Info("MCP 工具已接入", "tools", len(reg.Names()))
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
		// 关闭失败不影响退出：子进程已被终止，最坏是服务端那边留了个空会话。
		_ = c.Close()
	}
}

// connectMCP 并行连接所有启用的服务端。
//
// 连接失败不阻止启动：MCP 是扩展能力，某个服务端没起来时 agent 照样能用内置工具，
// 失败原因写进 MCPServerInfo.Err 交给 /tools 展示。
func connectMCP(ctx context.Context, f *config.MCPFile, e env, log *slog.Logger, versions *mcp.Versions) *mcpSet {
	names := f.EnabledServers()
	if len(names) == 0 {
		return nil
	}
	set := &mcpSet{servers: make([]tui.MCPServerInfo, len(names))}

	ctx, cancel := context.WithTimeout(ctx, mcpConnectTimeout)
	defer cancel()

	type result struct {
		idx    int
		spec   mcp.Spec
		client *mcp.Client
		err    error
	}
	ch := make(chan result, len(names))
	for i, name := range names {
		spec := mcpSpec(name, f.Servers[name], e.getenv)
		// 上次谈成的版本直接用来握手，跳过一轮注定失败的 server/discover 试探。
		spec.KnownVersion = versions.Lookup(spec)
		set.servers[i] = tui.MCPServerInfo{Name: name, URL: spec.URL, Command: spec.Command}
		go func() {
			c, err := mcp.Dial(ctx, spec, e.http, log)
			ch <- result{idx: i, spec: spec, client: c, err: err}
		}()
	}

	clients := make([]*mcp.Client, len(names))
	for range names {
		r := <-ch
		if r.err != nil {
			set.servers[r.idx].Err = r.err.Error()
			continue
		}
		clients[r.idx] = r.client
		set.servers[r.idx].Version = r.client.Version()
		set.servers[r.idx].Tools = countTools(r.client.Tools(), f.Servers[names[r.idx]])
		set.servers[r.idx].Server = r.client.ServerInfo().Name
		versions.Record(r.spec, r.client.Version())
	}
	for _, c := range clients {
		if c != nil {
			set.clients = append(set.clients, c)
		}
	}
	return set
}

// countTools 数服务端提供的工具里真正会注册给模型的个数（去掉 excludeTools 屏蔽的）。
func countTools(tools []mcp.ToolInfo, srv config.MCPServer) int {
	n := 0
	for _, t := range tools {
		if !srv.IsExcluded(t.Name) {
			n++
		}
	}
	return n
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
	if set == nil {
		return nil
	}
	var notes []string
	for _, c := range set.clients {
		name := c.Name()
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
	}
	return notes
}
