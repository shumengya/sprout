package config

import (
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"
)

// MCPServerNamePattern 是 mcp.jsonc 里服务端名字的合法形式。
//
// 名字会拼进工具名（mcp__<服务端>__<工具>），所以不能有空格、点号这类字符。
var MCPServerNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)

// MCPFile 对应 mcp.jsonc 的顶层结构。
type MCPFile struct {
	Servers map[string]MCPServer `json:"servers"`
}

// MCPServer 是一个 MCP 服务端的配置。url 与 command 二选一：
// 有 url 走 Streamable HTTP，有 command 则以子进程方式走 stdio。
type MCPServer struct {
	Enable bool `json:"enable"`
	// URL 是远程 MCP 端点，例如 https://mcp.example.com/mcp。
	URL string `json:"url"`
	// Headers 是 HTTP 请求头，放认证信息等。
	Headers map[string]string `json:"headers"`
	// Command / Args / Env / Cwd 用于启动本地 stdio 服务端。
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	Cwd     string            `json:"cwd"`
	// TimeoutSeconds 是单次请求超时，默认 60，最大 600。
	TimeoutSeconds int `json:"timeoutSeconds"`
	// ExcludeTools 是不交给模型的工具名（服务端上的原始名字）。
	ExcludeTools []string `json:"excludeTools"`
	// OAuth 非 nil 表示这个远程服务端用 OAuth 登录（sprout mcp login <名字>）；写 {} 即可，字段都可省略。
	OAuth *MCPOAuth `json:"oauth"`
}

// MCPOAuth 是 oauth 段。
type MCPOAuth struct {
	// ClientID 是在授权服务器上预先注册的客户端；不写时动态注册。
	ClientID string `json:"clientId"`
	// Scopes 是要申请的权限；不写时用服务端要求的。
	Scopes []string `json:"scopes"`
	// CallbackPort 是本机回调端口，默认随机；预先注册了固定 redirect_uri 的客户端需要指定。
	CallbackPort int `json:"callbackPort"`
}

// MCPPath 是 mcp.jsonc 的路径。
func MCPPath(dataDir string) string { return joinDataDir(dataDir, "mcp.jsonc") }

// LoadMCP 读取并解析 mcp.jsonc（与 models.jsonc 同样的 JSONC 规则）。
//
// 文件不存在时视为“没有配置 MCP 服务端”，返回空配置而不是错误：
// MCP 是可选扩展，不该因为缺文件而让 sprout 起不来。
func LoadMCP(path string) (*MCPFile, error) {
	var f MCPFile
	if err := decodeJSONC(path, &f); err != nil {
		// decodeJSONC 用 %w 包住了 os.ReadFile 的错误，这里能认出“文件不存在”。
		if errors.Is(err, fs.ErrNotExist) {
			return &MCPFile{}, nil
		}
		return nil, err
	}
	if probs := f.problems(); len(probs) > 0 {
		return nil, &ValidationError{Path: path, Problems: probs}
	}
	return &f, nil
}

// problems 返回所有校验问题，顺序稳定。
func (f *MCPFile) problems() []string {
	var out []string
	add := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }
	for _, name := range slices.Sorted(mapKeys(f.Servers)) {
		s := f.Servers[name]
		prefix := "servers." + name
		if !MCPServerNamePattern.MatchString(name) {
			add("%s 不是合法的服务端名：只能用字母或数字开头，后接字母、数字、下划线或连字符，最长 32 个字符", prefix)
		}
		hasURL, hasCmd := strings.TrimSpace(s.URL) != "", strings.TrimSpace(s.Command) != ""
		switch {
		case hasURL && hasCmd:
			add("%s 不能同时写 url 和 command：url 走远程 HTTP，command 走本地 stdio，二选一", prefix)
		case !hasURL && !hasCmd:
			if s.Enable {
				add("%s 已启用，但没有写 url 或 command，无法连接", prefix)
			}
		}
		if hasURL {
			if msg := checkBaseURL(s.URL); msg != "" {
				add("%s.url %s", prefix, msg)
			}
		}
		if hasCmd && len(s.Args) == 0 && strings.ContainsAny(s.Command, " \t") {
			add("%s.command 包含空格：stdio 服务端不经过 shell，请把程序路径写在 command，参数写进 args", prefix)
		}
		if s.TimeoutSeconds < 0 {
			add("%s.timeoutSeconds 不能为负数", prefix)
		}
		if s.TimeoutSeconds > 600 {
			add("%s.timeoutSeconds 不能超过 600", prefix)
		}
		if s.OAuth != nil {
			if hasCmd {
				add("%s.oauth 只用于 url 服务端：stdio 服务端不经过网络，凭证请放 env", prefix)
			}
			if p := s.OAuth.CallbackPort; p < 0 || p > 65535 {
				add("%s.oauth.callbackPort 必须在 0～65535 之间", prefix)
			}
		}
		for _, t := range s.ExcludeTools {
			if strings.TrimSpace(t) == "" {
				add("%s.excludeTools 里有空字符串", prefix)
			}
		}
	}
	return out
}

// mapKeys 返回 map 的键，配合 slices.Sorted 得到稳定顺序。
func mapKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// ServerNames 返回全部服务端名（含未启用的），按名字排序。
func (f *MCPFile) ServerNames() []string {
	names := make([]string, 0, len(f.Servers))
	for name := range f.Servers {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// EnabledServers 返回已启用的服务端名，按名字排序。
func (f *MCPFile) EnabledServers() []string {
	var out []string
	for name, s := range f.Servers {
		if s.Enable {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// IsExcluded 报告某个服务端上的工具是否被 excludeTools 屏蔽。
func (s MCPServer) IsExcluded(tool string) bool {
	for _, t := range s.ExcludeTools {
		if strings.EqualFold(strings.TrimSpace(t), tool) {
			return true
		}
	}
	return false
}
