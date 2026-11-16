package config

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// 内置工具名。tools.jsonc 只允许出现这些名字，写错时报错而不是静默失效。
const (
	ToolBash       = "bash"
	ToolPowerShell = "powershell"
	ToolRead       = "read"
	ToolWrite      = "write"
	ToolEdit       = "edit"
	ToolDelete     = "delete"
)

// KnownTools 是全部内置工具，顺序即模板与校验提示里的顺序。
var KnownTools = []string{ToolBash, ToolPowerShell, ToolRead, ToolWrite, ToolEdit, ToolDelete}

// 外部工具的 exec.shell 取值。
const (
	ExecBash       = "bash"
	ExecPowerShell = "powershell"
	ExecDirect     = "direct"
)

// ExecShells 列出 exec 支持的执行方式。
var ExecShells = []string{ExecBash, ExecPowerShell, ExecDirect}

// ToolNamePattern 是工具名的合法形式：模型要把它当 JSON 字段名用，限制在 ASCII 标识符范围内。
var ToolNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

// ToolsFile 对应 tools.jsonc 的顶层结构。
type ToolsFile struct {
	Tools map[string]ToolEntry `json:"tools"`
}

// ToolEntry 是一个工具的定义。
//
// 内置工具（bash / powershell / read / write）由程序实现，这里的 description 与 parameters
// 只用来覆盖内置的默认值，省略即用内置值；内置工具不允许写 exec。
//
// 其他名字是外部工具：必须给出 exec，把一次命令执行包装成工具。
type ToolEntry struct {
	Enable      bool            `json:"enable"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	// Parallel 省略时沿用工具自己的声明（外部工具默认可并行，write / edit / delete 默认不可）。
	// false 表示同一批里这个工具的调用独占执行，不和别的调用重叠。
	Parallel *bool     `json:"parallel"`
	Exec     *ExecSpec `json:"exec"`
}

// ExecSpec 描述外部工具怎么执行。命令里的 {{参数名}} 会替换成模型传入的值。
type ExecSpec struct {
	// Shell 是 bash / powershell / direct；direct 直接启动 command，不经 shell 解释。
	Shell string `json:"shell"`
	// Command 是要执行的命令。bash / powershell 下是完整命令行；direct 下是可执行文件。
	Command string `json:"command"`
	// Args 只在 direct 下使用，按顺序传给程序，每一项都可以含 {{参数名}}。
	Args []string `json:"args"`
	// TimeoutSeconds 省略时取 shell 工具的默认超时（120 秒），上限 600。
	TimeoutSeconds int `json:"timeoutSeconds"`
}

// IsBuiltin 报告名字是否是内置工具。
func IsBuiltin(name string) bool { return slices.Contains(KnownTools, name) }

// ToolsPath 是 tools.jsonc 的路径。
func ToolsPath(dataDir string) string { return joinDataDir(dataDir, "tools.jsonc") }

// LoadTools 读取并解析 tools.jsonc（与 models.jsonc 同样的 JSONC 规则）。
//
// 文件不存在时调用方应先 EnsureToolsTemplate；这里按 error 返回，让缺失显式暴露。
func LoadTools(path string) (*ToolsFile, error) {
	var f ToolsFile
	if err := decodeJSONC(path, &f); err != nil {
		return nil, err
	}
	if probs := f.problems(); len(probs) > 0 {
		return nil, &ValidationError{Path: path, Problems: probs}
	}
	return &f, nil
}

// problems 返回所有校验问题，顺序稳定。
func (f *ToolsFile) problems() []string {
	var out []string
	add := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }
	names := make([]string, 0, len(f.Tools))
	for name := range f.Tools {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		e := f.Tools[name]
		prefix := "tools." + name
		if !ToolNamePattern.MatchString(name) {
			add("%s 不是合法的工具名：只能用字母开头，后接字母、数字、下划线或连字符，最长 64 个字符", prefix)
		}
		if e.Exec == nil {
			if !IsBuiltin(name) {
				add("%s 不是内置工具（%s），必须提供 exec 说明如何执行", prefix, strings.Join(KnownTools, "、"))
				continue
			}
			// 内置工具覆盖参数时同样要校验：写错的 schema 会原样发给模型，被服务端拒掉整个请求。
			out = append(out, schemaProblems(prefix, e.Parameters)...)
			continue
		}
		if IsBuiltin(name) {
			add("%s 是内置工具，不能用 exec 覆盖；要改描述或参数请直接写 description / parameters", prefix)
			continue
		}
		if strings.TrimSpace(e.Description) == "" {
			add("%s.description 不能为空：模型靠它判断什么时候用这个工具", prefix)
			continue
		}
		out = append(out, e.Exec.problems(prefix+".exec")...)
		out = append(out, schemaProblems(prefix, e.Parameters)...)
	}
	return out
}

// schemaProblems 校验 parameters：没写时通过；写了就必须是 type 为 object 的 JSON 对象。
func schemaProblems(prefix string, raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return []string{fmt.Sprintf("%s.parameters 不是有效的 JSON Schema 对象: %v", prefix, err)}
	}
	if schema["type"] != "object" {
		return []string{fmt.Sprintf(`%s.parameters.type 必须是 "object"，当前是 %v`, prefix, schema["type"])}
	}
	return nil
}

// problems 校验 exec 本身。
func (x *ExecSpec) problems(prefix string) []string {
	var out []string
	add := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }

	if !slices.Contains(ExecShells, x.Shell) {
		add("%s.shell 必须是 %s 之一，当前是 %q", prefix, strings.Join(ExecShells, "、"), x.Shell)
	}
	if strings.TrimSpace(x.Command) == "" && len(x.Args) == 0 {
		add("%s.command 不能为空", prefix)
	}
	if x.Shell == ExecDirect && strings.TrimSpace(x.Command) == "" {
		add("%s.command 不能为空：direct 模式下它是可执行文件路径", prefix)
	}
	if x.Shell != "" && x.Shell != ExecDirect && len(x.Args) > 0 {
		add("%s.args 只在 shell 为 %q 时生效；%s 下请把参数写进 command", prefix, ExecDirect, x.Shell)
	}
	if x.TimeoutSeconds < 0 {
		add("%s.timeoutSeconds 不能为负数", prefix)
	}
	if x.TimeoutSeconds > 600 {
		add("%s.timeoutSeconds 不能超过 600", prefix)
	}
	return out
}

// Enabled 报告某个工具是否被显式启用。没出现在文件里同样视为未启用。
func (f *ToolsFile) Enabled(name string) bool {
	e, ok := f.Tools[name]
	return ok && e.Enable
}

// Entry 返回工具定义；没有这个工具时 ok 为 false。
func (f *ToolsFile) Entry(name string) (ToolEntry, bool) {
	e, ok := f.Tools[name]
	return e, ok
}

// Names 返回已启用的工具名，内置工具按 KnownTools 的顺序在前，外部工具按名字排序在后。
//
// 顺序稳定是为了让下发给模型的工具列表和会话记录保持一致。
func (f *ToolsFile) Names() []string {
	var out []string
	for _, name := range KnownTools {
		if f.Enabled(name) {
			out = append(out, name)
		}
	}
	var external []string
	for name := range f.Tools {
		if !IsBuiltin(name) && f.Enabled(name) {
			external = append(external, name)
		}
	}
	slices.Sort(external)
	return append(out, external...)
}

// EnabledNames 是 Names 的旧名字，保留避免调用方同步改动。
func (f *ToolsFile) EnabledNames() []string { return f.Names() }
