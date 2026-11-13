package main

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"time"

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
func connectMCP(ctx context.Context, f *config.MCPFile, e env, log *slog.Logger) *mcpSet {
	names := f.EnabledServers()
	if len(names) == 0 {
		return nil
	}
	set := &mcpSet{servers: make([]tui.MCPServerInfo, len(names))}

	ctx, cancel := context.WithTimeout(ctx, mcpConnectTimeout)
	defer cancel()

	type result struct {
		idx    int
		client *mcp.Client
		err    error
	}
	ch := make(chan result, len(names))
	for i, name := range names {
		spec := mcpSpec(name, f.Servers[name], e.getenv)
		set.servers[i] = tui.MCPServerInfo{Name: name, URL: spec.URL, Command: spec.Command}
		go func() {
			c, err := mcp.Dial(ctx, spec, e.http, log)
			ch <- result{idx: i, client: c, err: err}
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
