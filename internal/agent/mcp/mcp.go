// Package mcp 是一个最小可用的 MCP（Model Context Protocol）客户端：
// 支持 stdio 与 Streamable HTTP 两种传输，只处理工具（tools/list、tools/call）。
//
// 为什么自己写而不用官方 SDK：本项目的四个模型协议适配器都是手写的，MCP 需要的
// 只是 JSON-RPC 2.0 的收发与两次方法调用，引一个带 OAuth、JSON Schema、JWT 依赖的
// SDK 会破坏单二进制、少依赖的约束。
//
// 协议版本：先调 server/discover 试 2026-07-28 的无状态协议；调不通就退回
// 2025-06-18 的 initialize 握手。现网绝大多数服务端只认后者，而新规范要求服务端
// 至少保留 12 个月的向后兼容，所以两条路都是通的。
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 协议版本。
const (
	// ProtocolStateless 是无状态协议版本：没有 initialize 握手，每个请求自带 _meta。
	ProtocolStateless = "2026-07-28"
	// ProtocolLegacy 是退回 initialize 握手时声明的版本。
	ProtocolLegacy = "2025-06-18"
)

// DefaultTimeout 是单次请求的默认超时。
const DefaultTimeout = 60 * time.Second

// Spec 是一个 MCP 服务端的连接配置。URL 与 Command 二选一。
type Spec struct {
	// Name 是服务端名字，用于工具名前缀与展示。
	Name string
	// URL 非空时走 Streamable HTTP。
	URL     string
	Headers map[string]string
	// Command 非空时以子进程方式启动，走 stdio。
	Command string
	Args    []string
	Env     map[string]string
	Dir     string
	// Timeout 是单次请求的超时，<= 0 取 DefaultTimeout。
	Timeout time.Duration
	// KnownVersion 是上次连接时与服务端谈成的协议版本。非空时跳过 server/discover 试探，
	// 直接按这个版本握手；失败也只是一次额外往返，之后照常退回完整流程。
	KnownVersion string
	// Cache 是工具列表缓存；为 nil 时每次都重新 tools/list。
	Cache *ToolCache
	// OnToolsChanged 在服务端发来 notifications/tools/list_changed 后调用，参数是刷新后的工具个数。
	// 它在后台 goroutine 里执行，调用方要自己保证线程安全。
	OnToolsChanged func(server string, toolCount int)
}

// IsHTTP 报告这个服务端是否走 HTTP。
func (s Spec) IsHTTP() bool { return s.URL != "" }

func (s Spec) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return DefaultTimeout
}

// Client 是一个已建立连接的 MCP 客户端。方法可以并发调用：同一批工具调用会并行执行。
type Client struct {
	spec      Spec
	tr        transport
	version   string // 协商出的协议版本
	stateless bool   // 无状态协议：每个请求要在 _meta 里带版本与客户端信息
	info      ServerInfo
	log       *slog.Logger
	close     func()

	// tools、version 由 mu 保护：list_changed 通知会在后台刷新工具列表，同时别处在读。
	mu    sync.Mutex
	tools []ToolInfo
}

// ServerInfo 是服务端自报的身份。
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ToolInfo 是服务端提供的工具。
type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Content 是工具结果里的一段内容。
type Content struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
	URI      string `json:"uri"`
}

// CallResult 是 tools/call 的结果。
type CallResult struct {
	Content           []Content       `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent"`
	IsError           bool            `json:"isError"`
}

// Dial 连接一个服务端并完成协议协商与工具发现。
func Dial(ctx context.Context, spec Spec, hc *http.Client, log *slog.Logger) (*Client, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	c := &Client{spec: spec, log: log}

	var (
		tr  transport
		err error
	)
	if spec.IsHTTP() {
		if hc == nil {
			hc = &http.Client{}
		}
		tr = newHTTPTransport(spec, hc)
	} else {
		tr, err = newStdioTransport(spec)
		if err != nil {
			return nil, err
		}
	}
	c.tr = tr

	ctx, cancel := context.WithTimeout(ctx, spec.timeout())
	defer cancel()
	if err := c.handshake(ctx); err != nil {
		_ = tr.close()
		return nil, err
	}
	// 缓存仍在有效期内就直接用，省掉一次 tools/list；过期、换了目标或没有缓存时照常列。
	if cached, ok := spec.Cache.Lookup(spec); ok {
		c.tools = cached
	} else {
		tools, ttl, err := c.listTools(ctx)
		if err != nil {
			_ = tr.close()
			return nil, err
		}
		c.tools = tools
		c.rememberTools(ttl)
	}
	if spec.OnToolsChanged != nil {
		tr.setNotifyHook(c.onServerNotification)
	}
	return c, nil
}

// rememberTools 记下工具列表的缓存时长。ttlMs 缺省时用 DefaultToolTTL；
// 服务端明确给 0 表示不要缓存。
func (c *Client) rememberTools(ttl time.Duration) {
	if c.spec.Cache == nil {
		return
	}
	if ttl <= 0 {
		ttl = DefaultToolTTL
	}
	c.spec.Cache.Record(c.spec, c.Tools(), ttl)
}

// onServerNotification 在后台 goroutine 里处理服务端通知。目前只关心工具列表变了：
// 重新列一遍并回调，让上层重建 agent（工具 schema 烘在请求里，不重建就看不到新工具）。
func (c *Client) onServerNotification(method string) {
	if method != "notifications/tools/list_changed" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.spec.timeout())
	defer cancel()
	tools, ttl, err := c.listTools(ctx)
	if err != nil {
		c.log.Debug("工具列表变更后重新获取失败", "server", c.spec.Name, "error", err)
		return
	}
	c.mu.Lock()
	c.tools = tools
	c.mu.Unlock()
	c.rememberTools(ttl)
	c.log.Info("MCP 工具列表已变更", "server", c.spec.Name, "tools", len(tools))
	if c.spec.OnToolsChanged != nil {
		c.spec.OnToolsChanged(c.spec.Name, len(tools))
	}
}

// handshake 先按缓存的上次版本直连，失败或没有缓存时先试无状态发现，再退回 initialize。
func (c *Client) handshake(ctx context.Context) error {
	if v := c.spec.KnownVersion; v != "" {
		if err := c.handshakeKnown(ctx, v); err != nil {
			// 缓存可能过期（服务端升级、换了地址、令牌失效）。清掉试出来的状态再走完整流程，
			// 否则残留的版本头会让后面的请求继续被拒。
			c.log.Debug("按缓存版本握手失败，改为完整协商", "server", c.spec.Name, "version", v, "error", err)
			c.version, c.stateless, c.info = "", false, ServerInfo{}
			c.tr.setVersion("")
		} else {
			return nil
		}
	}

	params := map[string]any{
		"_meta": map[string]any{
			"io.modelcontextprotocol/protocolVersion":    ProtocolStateless,
			"io.modelcontextprotocol/clientInfo":         clientInfo(),
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		},
	}
	c.tr.setVersion(ProtocolStateless)
	if raw, err := c.tr.request(ctx, "server/discover", "", params); err == nil {
		var res struct {
			SupportedVersions []string   `json:"supportedVersions"`
			ServerInfo        ServerInfo `json:"serverInfo"`
		}
		if jsonErr := json.Unmarshal(raw, &res); jsonErr == nil && supportsVersion(res.SupportedVersions, ProtocolStateless) {
			c.version, c.stateless = ProtocolStateless, true
			c.info = res.ServerInfo
			return nil
		}
	}

	// 退回旧版握手。先试的 server/discover 已经把 2026-07-28 留在 HTTP 版本头里，
	// 退回前必须清掉，否则旧版服务端会因为不认识的版本号拒掉 initialize。
	c.tr.setVersion("")
	if err := c.initialize(ctx, ProtocolLegacy); err != nil {
		return fmt.Errorf("握手失败（server/discover 与 initialize 都不通）: %w", err)
	}
	return nil
}

// handshakeKnown 按上次谈成的版本直接握手，省掉 server/discover 的一次往返。
//
// 无状态版本本身就是靠 server/discover 建立的，按缓存走时要补一次（服务端身份也得从它拿）；
// 旧版版本则直接 initialize，这正是耗时的那一轮。
func (c *Client) handshakeKnown(ctx context.Context, v string) error {
	if v >= ProtocolStateless {
		c.tr.setVersion(ProtocolStateless)
		raw, err := c.tr.request(ctx, "server/discover", "", map[string]any{
			"_meta": map[string]any{
				"io.modelcontextprotocol/protocolVersion":    ProtocolStateless,
				"io.modelcontextprotocol/clientInfo":         clientInfo(),
				"io.modelcontextprotocol/clientCapabilities": map[string]any{},
			},
		})
		if err != nil {
			return err
		}
		var res struct {
			SupportedVersions []string   `json:"supportedVersions"`
			ServerInfo        ServerInfo `json:"serverInfo"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return err
		}
		if !supportsVersion(res.SupportedVersions, ProtocolStateless) {
			return fmt.Errorf("服务端不再支持 %s", ProtocolStateless)
		}
		c.version, c.stateless, c.info = ProtocolStateless, true, res.ServerInfo
		return nil
	}
	c.tr.setVersion("")
	return c.initialize(ctx, v)
}

// initialize 走旧版握手：声明版本、接受服务端谈成的版本，再发 initialized 通知。
func (c *Client) initialize(ctx context.Context, declared string) error {
	// 服务端可能谈成比我们声明的更低的版本，原样记下即可。
	raw, err := c.tr.request(ctx, "initialize", "", map[string]any{
		"protocolVersion": declared,
		"capabilities":    map[string]any{},
		"clientInfo":      clientInfo(),
	})
	if err != nil {
		return err
	}
	var res struct {
		ProtocolVersion string     `json:"protocolVersion"`
		ServerInfo      ServerInfo `json:"serverInfo"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("解析 initialize 结果失败: %w", err)
	}
	c.version = res.ProtocolVersion
	if c.version == "" {
		c.version = declared
	}
	c.info = res.ServerInfo
	// 谈定的版本要立刻用于后续请求的 MCP-Protocol-Version 头。
	c.tr.setVersion(c.version)
	// initialized 通知失败不影响后续请求：有的服务端不等它就开始工作。
	if err := c.tr.notify(ctx, "notifications/initialized", nil); err != nil {
		c.log.Debug("发送 notifications/initialized 失败", "server", c.spec.Name, "error", err)
	}
	return nil
}

func clientInfo() map[string]any {
	return map[string]any{"name": "sprout", "version": "1.0"}
}

func supportsVersion(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// Name 返回配置里的服务端名。
func (c *Client) Name() string { return c.spec.Name }

// Version 返回协商出的协议版本。
func (c *Client) Version() string { return c.version }

// ServerInfo 返回服务端自报的身份。
func (c *Client) ServerInfo() ServerInfo { return c.info }

// listTools 取全部工具，跟着 nextCursor 翻页，同时返回服务端给的缓存时长（ttlMs）。
// 没有给 ttlMs 时返回 0，由调用方决定默认值。
func (c *Client) listTools(ctx context.Context) ([]ToolInfo, time.Duration, error) {
	var out []ToolInfo
	cursor := ""
	var ttl time.Duration
	for range 20 { // 防止服务端返回环状的 cursor 时死循环
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.tr.request(ctx, "tools/list", "", c.wrap(params))
		if err != nil {
			return nil, 0, fmt.Errorf("获取工具列表失败: %w", err)
		}
		var res struct {
			Tools      []ToolInfo `json:"tools"`
			NextCursor string     `json:"nextCursor"`
			// ttlMs 是服务端建议的缓存时长（毫秒），0 表示不要缓存。
			TTLMs *int64 `json:"ttlMs"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, 0, fmt.Errorf("解析工具列表失败: %w", err)
		}
		if res.TTLMs != nil && ttl == 0 {
			ttl = time.Duration(*res.TTLMs) * time.Millisecond
		}
		out = append(out, res.Tools...)
		if res.NextCursor == "" || res.NextCursor == cursor {
			break
		}
		cursor = res.NextCursor
	}
	return out, ttl, nil
}

// Tools 返回发现到的工具。
func (c *Client) Tools() []ToolInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ToolInfo(nil), c.tools...)
}

// Call 调用一个工具。
func (c *Client) Call(ctx context.Context, name string, args json.RawMessage) (CallResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.spec.timeout())
	defer cancel()
	params := map[string]any{"name": name}
	if len(args) > 0 {
		var v any
		if err := json.Unmarshal(args, &v); err != nil {
			return CallResult{}, fmt.Errorf("参数不是有效的 JSON: %w", err)
		}
		params["arguments"] = v
	}
	raw, err := c.tr.request(ctx, "tools/call", name, c.wrap(params))
	if err != nil {
		return CallResult{}, err
	}
	var res CallResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return CallResult{}, fmt.Errorf("解析工具结果失败: %w", err)
	}
	return res, nil
}

// wrap 在无状态协议下给参数补 _meta；旧版协议原样返回。
func (c *Client) wrap(params map[string]any) map[string]any {
	if !c.stateless {
		return params
	}
	params["_meta"] = map[string]any{
		"io.modelcontextprotocol/protocolVersion":    ProtocolStateless,
		"io.modelcontextprotocol/clientInfo":         clientInfo(),
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
	return params
}

// Close 结束连接。对 stdio 是关闭子进程，对 HTTP 是尽量发 DELETE 结束会话。
func (c *Client) Close() error {
	if c.tr == nil {
		return nil
	}
	return c.tr.close()
}

// transport 是两种传输的共同接口。name 只用于 Mcp-Name 头与诊断，stdio 忽略它。
type transport interface {
	request(ctx context.Context, method, name string, params any) (json.RawMessage, error)
	notify(ctx context.Context, method string, params any) error
	// setVersion 记下协商出的协议版本；空串表示按旧版处理，不再发版本头。
	setVersion(v string)
	// setNotifyHook 注册服务端通知的处理函数：传输层收到带 method、不带 id 的消息时调用它。
	setNotifyHook(fn func(method string))
	close() error
}

// ToolName 是 MCP 工具注册给模型的名字：mcp__<服务端>__<工具>。
// 两段都按模型的工具名约束清洗，整体再截到 64 个字符。
func ToolName(server, tool string) string {
	return sanitizeToolName(ToolPrefix + server + "__" + tool)
}

// sanitizeToolName 把工具名变成符合模型工具命名约束的名字：
// 字母开头，只含字母、数字、下划线、连字符，最长 64。
func sanitizeToolName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || !isLetter(out[0]) {
		out = "t_" + out
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
