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
//
// 只有 enable 为 true 的工具才会被 agent 发现和调用；改成 false 或删掉条目即关闭。
// 修改后需要重启 sprout 才会生效。
//
// 每个条目可以写：
//   enable       是否启用（必填 true 才算）
//   description  给模型看的工具说明。内置工具不写则用程序自带的描述
//   parameters   参数 JSON Schema（type 必须是 object）。内置工具不写则用程序自带的参数
//   exec         外部工具的执行方式；只有非内置工具需要，内置工具写了会报错
//
// exec 的写法：
//   "shell": "bash" | "powershell" | "direct"
//   "command": 要执行的命令；bash / powershell 下是完整命令行，direct 下是可执行文件
//   "args":    仅 direct 使用，按顺序传给程序
//   "timeoutSeconds": 可选，默认 120，最大 600
// 命令里的 {{参数名}} 会替换成模型传入的值；bash / powershell 下会自动加引号转义。
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

    // 外部工具示例：把 ls 包装成一个工具。
    // 非内置名字必须给出 description 与 exec，否则启动时报错。
    // {{path}} 会替换成模型传入的 path；bash 下自动加引号，所以路径带空格也安全。
    "ls": {
      "enable": true,
      "description": "列出目录内容，返回权限、大小和修改时间，不递归。",
      "parameters": {
        "type": "object",
        "properties": {
          "path": { "type": "string", "description": "要列出的目录，传 \".\" 表示当前工作目录。" }
        },
        "required": ["path"]
      },
      "exec": { "shell": "bash", "command": "ls -la {{path}}" }
    },

    // 不给参数的直接命令（direct 模式，不经 shell 解释）：
    // "git-status": {
    //   "enable": false,
    //   "description": "显示 git 工作区状态。",
    //   "exec": { "shell": "direct", "command": "git", "args": ["status", "--short"] }
    // },
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
