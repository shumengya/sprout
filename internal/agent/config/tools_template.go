package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// ToolsTemplate 是首次运行时生成的 tools.jsonc。
//
// 规则：只有出现在这里且 "enable": true 的工具才会交给 agent。
// 没出现的工具视为未注册；写错名字会在启动时直接报错，避免拼写错误导致工具悄悄失效。
const ToolsTemplate = `// Sprout 工具配置（JSONC：支持 // 和 /* */ 注释，以及尾逗号）。
// 只有 enable 为 true 的工具才会被 agent 发现和调用；改成 false 或删掉条目即关闭。
// 修改后需要重启 sprout 才会生效。
{
  "tools": {
    // bash：执行一条命令或脚本，返回 stdout、stderr 和退出码。
    // Windows 上只认真实的 Git Bash / MSYS2 / Cygwin；找不到时启动会在 /tools 里说明原因。
    "bash": { "enable": true },

    // powershell：Windows 上优先 pwsh（7+），回退 Windows PowerShell 5.1。
    "powershell": { "enable": true },

    // read：读取文本文件，按行号返回，可用 offset / limit 只读一部分。
    "read": { "enable": true },

    // write：写入（覆盖）文本文件，自动创建缺失的父目录。
    "write": { "enable": true },
  },
}
`

// EnsureToolsTemplate 在 path 不存在或为空时写入模板，已有内容则原样保留。
//
// 空文件也要写：早期版本会在数据目录预建一批空的配置文件占位，空文件不是可用的配置，
// 而 LoadTools 会把空内容当成语法错误。
func EnsureToolsTemplate(path string) (created bool, err error) {
	if info, statErr := os.Stat(path); statErr == nil && info.Size() > 0 {
		return false, nil
	} else if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
		return false, fmt.Errorf("检查工具配置失败: %w", statErr)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return false, fmt.Errorf("创建工具配置模板失败: %w", err)
	}
	if _, err := f.WriteString(ToolsTemplate); err != nil {
		f.Close()
		return false, fmt.Errorf("写入工具配置模板失败: %w", err)
	}
	return true, f.Close()
}

// toolsTemplateMatches 供测试比较模板与仓库里的示例文件，统一换行后再比。
func toolsTemplateMatches(b []byte) bool {
	return bytes.Equal(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")), []byte(ToolsTemplate))
}
