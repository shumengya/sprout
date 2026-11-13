package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// MCPTemplate 是首次运行时生成的 mcp.jsonc。
// 仓库根目录的 mcp.example.jsonc 必须与它逐字一致，由 mcp_test.go 保证。
//
// 模板里一个服务端都不启用：探测失败的远程服务会让每次启动都多等一轮超时，
// 用户想接什么自己打开注释即可。
const MCPTemplate = `// Sprout MCP 配置（JSONC：支持 // 和 /* */ 注释，以及尾逗号）。
// 修改后需要重启 sprout 才会生效。
//
// MCP（Model Context Protocol）让 agent 用上外部服务端提供的工具。
// 每个服务端二选一：
//   url      远程端点，走 Streamable HTTP
//   command  本地程序，以子进程方式启动，走 stdio（args / env / cwd 配合）
//
// 每个服务端可以写：
//   enable         是否启用（必须显式写 true）
//   timeoutSeconds 单次请求超时，默认 60，最大 600
//   headers        远程服务端的请求头，用来放令牌等
//   excludeTools   不交给模型的工具名（服务端上的原始名字）
//
// 服务端提供的工具会以 mcp__<服务端名>__<工具名> 的名字注册给模型；
// 与内置工具或其他服务端重名时只保留先注册的那个，并在 /tools 里说明。
{
  "servers": {
    // 远程服务端示例：接一个托管式的搜索服务。
    // "tavily": {
    //   "enable": true,
    //   "url": "https://mcp.example.com/mcp",
    //   "headers": { "Authorization": "Bearer 你的令牌" },
    //   "timeoutSeconds": 60,
    //   "excludeTools": ["tavily_crawl"]
    // },

    // 本地服务端示例：npx 启动的社区服务端。apiKey 这类机密建议放在 env 里，
    // 用 ${环境变量名} 引用当前进程的环境变量，避免明文写进配置文件。
    // "filesystem": {
    //   "enable": true,
    //   "command": "npx",
    //   "args": ["-y", "@modelcontextprotocol/server-filesystem", "D:/work"],
    //   "env": { "API_KEY": "${MY_API_KEY}" }
    // }
  }
}
`

// EnsureMCPTemplate 在 path 不存在或为空时写入模板，已有内容则原样保留。
func EnsureMCPTemplate(path string) (created bool, err error) {
	if info, statErr := os.Stat(path); statErr == nil && info.Size() > 0 {
		return false, nil
	} else if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
		return false, fmt.Errorf("检查 MCP 配置失败: %w", statErr)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return false, fmt.Errorf("创建 MCP 配置模板失败: %w", err)
	}
	if _, err := f.WriteString(MCPTemplate); err != nil {
		f.Close()
		return false, fmt.Errorf("写入 MCP 配置模板失败: %w", err)
	}
	return true, f.Close()
}

// mcpTemplateMatches 供测试比较模板与仓库里的示例文件，统一换行后再比。
func mcpTemplateMatches(b []byte) bool {
	return bytes.Equal(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")), []byte(MCPTemplate))
}
