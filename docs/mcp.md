# MCP

Sprout 内置一个最小化的 MCP（Model Context Protocol）客户端，把外部服务端提供的工具注册给模型，名字统一是 `mcp__<服务端>__<工具>`。

协议参考：[modelcontextprotocol.io](https://modelcontextprotocol.io)。当前规范版本 `2026-07-28`，从这一版起协议改为无状态核心（没有 `initialize` 握手、没有 `Mcp-Session-Id`）。

## 为什么自己实现

内置的四个模型协议适配器都是手写的，MCP 需要的只是 JSON-RPC 2.0 的收发和两个方法（`tools/list`、`tools/call`）。引官方 SDK 会带进 OAuth、JSON Schema、JWT、golang.org/x/tools 等一串依赖，与“单二进制、无 CGO、Termux 可编译”的约束相悖。所以 `internal/agent/mcp` 自己写了两种传输，合计约 900 行。

## 配置：`mcp.jsonc`

放在数据目录（默认 `<exeDir>/data/.sprout/mcp.jsonc`），首次启动生成一份全部注释掉的模板。JSONC：支持 `//`、`/* */` 与尾逗号，与 `models.jsonc` 同一套解析和报错定位。仓库根的 `mcp.example.jsonc` 与内置模板逐字一致，由测试保证。

```jsonc
{
  "servers": {
    // 远程：走 Streamable HTTP
    "tavily": {
      "enable": true,
      "url": "https://mcp.example.com/mcp",
      "headers": { "Authorization": "Bearer ${TAVILY_API_KEY}" },
      "timeoutSeconds": 60,
      "excludeTools": ["tavily_crawl"]
    },

    // 本地：以子进程方式启动，走 stdio
    "filesystem": {
      "enable": true,
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "D:/work"],
      "env": { "API_KEY": "${MY_API_KEY}" },
      "cwd": ""
    }
  }
}
```

| 字段 | 说明 |
| --- | --- |
| `enable` | 必须显式写 `true` 才连接 |
| `url` | 远程端点，走 Streamable HTTP；与 `command` 二选一 |
| `headers` | 远程服务端的请求头 |
| `command` / `args` / `env` / `cwd` | 本地服务端。`command` 不经 shell，含空格会被拒绝（请写程序路径 + `args`） |
| `timeoutSeconds` | 单次请求超时，默认 60，最大 600 |
| `excludeTools` | 不注册的工具（服务端上的原始名字，大小写不敏感） |

`${VAR}` 会展开成当前进程的环境变量（`url`、`args`、`env`、`headers` 里的值），用来避免把令牌明文写进配置文件。展开发生在装载阶段，启动后改环境变量不影响已连接的会话。

配置缺失视为“没有 MCP 服务端”，不是错误；字段写错（同时写了 `url` 与 `command`、启用了却没写地址、URL 不是 http/https、服务端名不合法）会在启动时报出全部问题并退出。

## 协议与传输

### 握手

`sprout` 先按新规范调 `server/discover`，`_meta` 里带 `io.modelcontextprotocol/protocolVersion: "2026-07-28"`、`clientInfo`、`clientCapabilities`。服务端支持的版本里包含 `2026-07-28` 就进入**无状态模式**：后续每个请求都在 `_meta` 里重复这三项，HTTP 请求另外带 `Mcp-Method`、`Mcp-Name`、`MCP-Protocol-Version` 头。

否则（大多数现网服务端）退回旧版：`initialize` 声明 `2025-06-18`，接受服务端谈成的任何版本，再发 `notifications/initialized`。此时 HTTP 请求带 `Mcp-Session-Id`（服务端下发时）与协商出的 `MCP-Protocol-Version`。

退回前会清掉刚才试 `server/discover` 时留下的版本头——旧版服务端会因为不认识的版本号直接拒掉 `initialize`。

### 传输

| 传输 | 实现 |
| --- | --- |
| stdio | `exec.CommandContext` 启动子进程，stdout 每行一条 JSON-RPC 消息，stderr 只保留末尾 4KB 用于报错时回显；关 stdin → 等 2 秒 → 终止整个进程树（复用 `platform.ConfigureCmd`） |
| Streamable HTTP | 每个请求一次 POST，`Accept: application/json, text/event-stream`；响应是 JSON 或 SSE 都支持，SSE 里跳过通知、按 id 匹配真正的响应；关闭时尽量发 DELETE 结束会话 |

stdio 的读循环按 id 分发响应到等待者，所以同一批工具调用可以并发。服务端发来的 `ping`、`roots/list` 会随手回掉，其余请求回“方法不支持”，避免服务端干等。

### 工具

`tools/list` 会跟着 `nextCursor` 翻页（最多 20 页，防止环状 cursor）。工具名清洗后拼上前缀：`mcp__` + 服务端名 + `__` + 原始工具名，只保留字母、数字、`_`、`-`，字母开头，最长 64 字符（`mcp.ToolName`）。

`tools/call` 的结果按内容类型转成文本：`text` 直接拼接，图片/音频/资源链接写成 `[图片内容：image/png]` 这样的占位（`tool.Result` 只能承载文本）；`structuredContent` 附在后面；`isError: true` 映射成退出码 1，让模型看到失败但不会中断 agent。

冲突：与内置工具、外部工具、其他 MCP 工具重名时先注册的赢（内置 > 外部 > MCP），被跳过的那条写在 `/tools` 的不可用列表里。

## 启动与生命周期

- 只连接 `enable: true` 的服务端；模型 `tools=false` 时完全不连——注册了也发不下去。
- **连接在后台进行**：启动时立即显示界面，状态栏的 MCP 一项显示 `连接中`；连好后工具自动接进 agent，界面里的对话历史和上下文摘要原样保留，不需要重启。生成中连好的会等到这一轮结束再接，避免重建 agent 时丢掉正在进行的消息。
- 这期间提问也能用，只是暂时没有 MCP 工具；内置工具照常工作。
- 所有服务端并行连接，整体上限 20 秒；单个服务端按自己的 `timeoutSeconds` 超时。任一个失败都不阻止 sprout 启动，失败原因出现在对话区顶部提示、`/tools` 与状态栏。
- `sprout run` 是单次提问，会先等连接结束再问，保证这一次能用上 MCP 工具。
- 连接只发生在启动时；改完 `mcp.jsonc` 需要重启 sprout。
- 退出时关闭全部连接（stdio 终止子进程，HTTP 尽力 DELETE）。

### 协议版本缓存

每个服务端上次谈成的协议版本记在 `<数据目录>/mcp-versions.json`，下次启动直接按它握手，省掉一轮注定失败的 `server/discover` 试探（现网服务端几乎都只认旧版握手）。缓存不是权威：连接目标（地址或命令 + 参数）变了就自动作废；按缓存握手失败时退回完整协商。缓存里存的是目标指纹的 SHA-256，不写地址原文，避免 `${VAR}` 展开出的令牌落盘。删掉这个文件没有副作用，只是下次多一次往返。

## 界面

- 状态栏第三行“MCP”显示 `已连接/已配置`，例如 `2/3`；连接进行中显示 `连接中`；没有配置任何服务端时显示 `—`。
- `/tools` 列出每个服务端：工具数、地址或命令、协商出的协议版本、连接失败原因。
- `sprout run` 把连接失败写到 stderr。

## 已知限制

- 只支持工具（`tools`）。Resources、Prompts、Sampling、Elicitation 都没实现；服务端发起的 `elicitation/create` 等请求一律回“方法不支持”。
- 工具结果里的图片不会转成图片块发给模型，只写占位文本。
- 多轮请求（`resultType: "input_required"`，MRTR）不处理：服务端要求中途补充输入时会返回错误文本。
- 没有工具列表缓存：每次启动重新发现。`tools/list` 的 `ttlMs` / `cacheScope` 目前忽略。协议版本本身有缓存（见上文），跳过的是探测，不是 `tools/list`。
- 没有 OAuth：远程服务端需要令牌时，自己用 `headers` 配。
- 工具级开关只有 `excludeTools`，没有 WebUI 管理面板（WebUI 是 M4）。
- 没有 `resources/subscribe`、`notifications/tools/list_changed` 的处理：服务端工具列表变化要重启才发现。

## 代码位置

- `internal/agent/mcp/mcp.go`：客户端、握手与版本协商、工具列表、调用
- `internal/agent/mcp/stdio.go`、`http.go`：两种传输
- `internal/agent/mcp/jsonrpc.go`：JSON-RPC 消息结构
- `internal/agent/mcp/tool.go`：把 MCP 工具适配成 `tool.Tool`
- `internal/agent/config/mcp.go`、`mcp_template.go`：`mcp.jsonc` 的解析、校验与模板
- `cmd/sprout/mcp.go`：并行连接、环境变量展开、工具注册

## 测试

- `internal/agent/mcp/mcp_test.go`：旧版 HTTP 握手与调用、无状态 `server/discover`、SSE 响应、HTTP 错误、工具名清洗。
- `internal/agent/mcp/stdio_test.go`：真实子进程充当服务端（测试二进制自举），验证握手、分页外的调用、`isError`、命令不存在、超时。
- `internal/agent/config/mcp_test.go`：模板合法、示例与模板一致、缺文件不报错、校验一次报全、按 enable 过滤。
- `cmd/sprout/mcp_test.go`：`${VAR}` 展开、`excludeTools` 生效、注册名前缀、连接失败只记录不致命。
