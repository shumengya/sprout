package config

import (
	"fmt"
	"slices"
	"strings"
)

// 内置工具名。tools.jsonc 只允许出现这些名字，写错时报错而不是静默失效。
const (
	ToolBash       = "bash"
	ToolPowerShell = "powershell"
	ToolRead       = "read"
	ToolWrite      = "write"
)

// KnownTools 是全部内置工具，顺序即模板与校验提示里的顺序。
var KnownTools = []string{ToolBash, ToolPowerShell, ToolRead, ToolWrite}

// ToolsFile 对应 tools.jsonc 的顶层结构。
type ToolsFile struct {
	Tools map[string]ToolEntry `json:"tools"`
}

// ToolEntry 是一个工具的注册项。没写 enable 或写 false 都表示不启用。
type ToolEntry struct {
	Enable bool `json:"enable"`
}

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
	names := make([]string, 0, len(f.Tools))
	for name := range f.Tools {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if !slices.Contains(KnownTools, name) {
			out = append(out, fmt.Sprintf("tools.%s 不是已知工具，可用工具：%s", name, strings.Join(KnownTools, "、")))
		}
	}
	return out
}

// Enabled 报告某个工具是否被显式启用。没出现在文件里同样视为未启用。
func (f *ToolsFile) Enabled(name string) bool {
	e, ok := f.Tools[name]
	return ok && e.Enable
}

// EnabledNames 返回已启用的工具名，按 KnownTools 的顺序排列，保证下发给模型的工具顺序稳定。
func (f *ToolsFile) EnabledNames() []string {
	var out []string
	for _, name := range KnownTools {
		if f.Enabled(name) {
			out = append(out, name)
		}
	}
	return out
}
