# M1 最小实现（MVP-0）

> 目标：一个能在 Windows 上双击运行、以 TUI 为主入口的迷你 agent。
> 只验证一件事：用户 ↔ 模型 ↔ 终端工具这条闭环能不能稳定跑起来。

## 1. 目标

- Windows 双击 `sprout.exe` 打开终端 TUI。
- 输入一句话，模型通过 OpenAI Chat Completions 或 Responses 协议回答。
- 需要时调用 `powershell` 或 `bash`，把结果回给模型，继续推理直到给出最终答案。
- 会话按工作目录写入 JSONL，可事后查看。
- Provider 同时支持 OpenAI Chat Completions 和 Responses；Anthropic 在后续里程碑补。

## 2. 用户看到的样子

```text
┌ Sprout ─────────────────────────────────────────────────────────────┐
│ model: deepseek-chat   cwd: D:\SmyProjects\sprout                    │
│ tools: powershell, bash                                              │
├──────────────────────────────────────────────────────────────────────┤
│ 你> 看看当前目录有哪些文件                                            │
│                                                                      │
│ ⚙ powershell  Get-ChildItem                                         │
│   └ 14 行，exit 0                                                   │
│                                                                      │
│ 当前目录下有 14 个文件，主要是 Go 源码和文档……                        │
├──────────────────────────────────────────────────────────────────────┤
│ 状态: 空闲                                      Ctrl+C 取消 / 退出    │
│ >                                                                    │
└──────────────────────────────────────────────────────────────────────┘
```

- 启动后进入全屏 TUI，显示模型、工作目录、数据目录、检测到的终端工具。
- 输入框回车发送；生成期间显示 spinner；流式追加回答。
- 工具调用以一行标题 + 截断后的输出预览展示。
- `Ctrl+C`：正在生成时取消本轮；空闲时退出。`Ctrl+D` 退出。
- 不做 Markdown 渲染、不做鼠标、不做多窗口。

## 3. 功能范围

| 包含 | 不包含 |
| --- | --- |
| Bubble Tea v2 TUI（主入口） | WebUI、JSON-RPC、daemon |
| `sprout run "..."` 单次 CLI（调试/脚本用） | 交互式 CLI REPL |
| OpenAI Chat Completions + Responses 流式 | Anthropic Messages |
| ReAct 循环，最多 10 次工具调用 | 上下文压缩、token 预算 |
| `bash`、`powershell` 两个工具 | 文件读写、搜索、web、MCP、skills |
| `models.jsonc` 用户配置 | 配置热加载、模型选择器 |
| JSONL 会话，按工作目录分组 | 会话恢复、分支、搜索 |
| Windows 控制台 UTF-8、双击运行 | 权限审批、沙箱 |

### 3.1 两种 OpenAI 协议的归一化

内部只维护一种消息模型：`system` / `user` / `assistant` / `tool`，工具调用统一为 `ToolCall{ID, Name, Arguments}`。provider 适配器负责在协议和内部模型之间转换：

- **Chat Completions**：`messages[]` + `tools[].function`；流式读 `choices[0].delta.content` 和 `delta.tool_calls[]`；工具结果作为 `role:"tool"` 消息回传。
- **Responses**：`instructions` + `input[]`；工具声明是扁平的 `{type:"function", name, description, parameters}`；文本流读 `response.output_text.delta`；函数调用读 `response.output_item.added` / `response.function_call_arguments.delta` / `response.function_call_arguments.done`，或直接从 `response.output_item.done` 拿完整 item；工具结果作为 `{type:"function_call_output", call_id, output}` 追加到 input。
- Responses 使用 `store:false` + 完整 input，保持无服务端状态，和 Chat Completions 的“每次带全量 messages”行为一致。
- 两个适配器都只向外发统一事件：`message.delta`、`tool.requested`、`tool.finished`、`turn.finished`；agent 循环不感知协议差异。

## 4. 配置格式约定

- **用户会打开、编辑的文件一律 JSONC**：允许 `//`、`/* */` 注释和尾逗号。包括 `models.jsonc`，以及后续的 `config.jsonc`、`mcp.jsonc`、`skills.jsonc`。
- **用户很少看、机器读写的文件用普通 JSON / JSONL**：`meta.json`、`sessions/*.jsonl`、缓存、临时状态。
- 解析 JSONC 用 `github.com/tailscale/hujson`，解析失败时报告文件路径、行、列。
- 首次运行如果 `<exeDir>/data/.sprout/models.jsonc` 不存在，从内置模板生成一份带注释的配置，TUI 提示用户填入 API Key 后重启。
- 数据目录默认 `<exeDir>/data/.sprout`，是安装级/系统级目录，不跟随用户 home，也不跟随项目；`--data-dir` 或 `SPROUT_DATA_DIR` 可覆盖。

示例：

```jsonc
{
  // 默认使用的 provider/model
  "default": "deepseek/deepseek-chat",

  "providers": {
    "deepseek": {
      "protocol": "openai-chat",
      "baseUrl": "https://api.deepseek.com/v1",
      // 优先读环境变量；也可以直接写 apiKey，文件仅在本机使用
      "apiKeyEnv": "DEEPSEEK_API_KEY",
      "apiKey": "",
      "models": {
        "deepseek-chat": {
          "contextWindow": 128000,
          "maxOutput": 8192,
          "tools": true
        }
      }
    },

    "openai": {
      "protocol": "openai-chat",
      "baseUrl": "https://api.openai.com/v1",
      "apiKeyEnv": "OPENAI_API_KEY",
      "models": {
        "gpt-4o-mini": {
          "contextWindow": 128000,
          "maxOutput": 16384,
          "tools": true
        }
      }
    },

    "openai-responses": {
      "protocol": "openai-responses",
      "baseUrl": "https://api.openai.com/v1",
      "apiKeyEnv": "OPENAI_API_KEY",
      "models": {
        "gpt-5": {
          "contextWindow": 400000,
          "maxOutput": 32768,
          "tools": true
        }
      }
    }
  },

  "shell": {
    // 留空则自动探测；Windows 下可填 Git Bash / PowerShell 的绝对路径
    "bashPath": "",
    "powershellPath": ""
  }
}
```

## 5. 终端工具：bash 与 powershell 都是一等公民

两个工具都要实现，不排先后。是否可用由平台和能力检测决定，模型只看到当前真实可用的工具。

### Windows

PowerShell 检测顺序（参考 Claude Code / pi）：

1. 配置里的 `shell.powershellPath`
2. `pwsh.exe` on PATH
3. `%ProgramFiles%\PowerShell\7\pwsh.exe`
4. `powershell.exe` on PATH
5. `%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe`

- 记录实际找到的路径和版本（`pwsh` = 7+，`powershell` = 5.1）。
- 工具描述里带上版本差异：5.1 不支持 `&&` / `||` / 三元 / `??`，默认输出 UTF-16LE；7+ 支持现代语法。

Bash 检测顺序（参考 pi）：

1. 配置里的 `shell.bashPath`（`models.jsonc`）
2. `%ProgramFiles%\Git\bin\bash.exe`
3. `%ProgramFiles(x86)%\Git\bin\bash.exe`
4. PATH 里的 `bash.exe`（MSYS2 / Cygwin）
5. 只有 `C:\Windows\System32\bash.exe`（WSL 启动器）时视为不可用，并提示安装 Git for Windows

- Windows 上的 `bash` 明确是 Git Bash：使用 POSIX 路径（`/d/...`），不假设有 WSL。
- 找不到 Git Bash 时不注册 `bash` 工具，TUI 里显示“bash 不可用：未找到 Git Bash”。

### Linux / macOS / Termux

- `bash` 从 PATH 查找，始终注册。
- `powershell` 不注册（按当前决策，PowerShell 工具只在 Windows 提供）。

### 执行契约（两个工具共用）

- 每次调用新进程，工作目录固定为启动目录，`cd` 不跨调用保持。
- 默认超时 120 秒，可配置到 600 秒。
- 返回 `stdout`、`stderr`、`exit_code`、`duration`、`truncated`。
- 输出上限 50KB：保留头尾，中间插入截断标记。
- 不接管 stdin，不支持后台任务，不支持交互式命令。
- 超时、退出码非 0、命令不存在都作为工具结果回给模型，不直接终止 agent。

### 系统提示词

- 只列出实际可用的终端工具。
- Windows 同时有两者时说明：系统命令优先 `powershell`，POSIX 脚本或用户明确要求时用 `bash`。
- 告知模型两个工具都是无状态的，每次都要从工作目录重新开始。

## 6. TUI 设计

技术栈：Bubble Tea v2 + lipgloss + bubbles。单模型，子组件不各自跑 `Update`。

### 布局

- 顶部：模型、cwd、数据目录、可用工具。
- 中间：transcript viewport，自动跟随底部；用户上滚后暂停跟随。
- 底部：状态行 + 输入框（`bubbles/textarea`）。

### 数据流

```text
TUI 输入 → agent.Run(ctx, prompt) → event channel → tea.Cmd → TUI 更新
```

- `Update` 里不做 IO、不发请求；所有副作用通过 `tea.Cmd`。
- agent 在独立 goroutine 运行，事件写入 channel；TUI 用可重复挂起的 `waitForEvent` 命令消费。
- 事件：`turn.started`、`message.delta`、`tool.requested`、`tool.finished`、`turn.finished`、`error`。
- `message.delta` 追加到当前 assistant 条目；`tool.requested/finished` 生成工具条目。
- 取消：保存当前 turn 的 `context.CancelFunc`，`Ctrl+C` 调用。

### 最小交互

| 按键 | 行为 |
| --- | --- |
| `Enter` | 发送 |
| `Ctrl+J` | 输入框换行 |
| `Ctrl+C` | 生成中：取消本轮；空闲：退出 |
| `Ctrl+D` | 退出 |
| `PgUp/PgDn` | 滚动 transcript |

## 7. 会话存储

- 路径：`<dataDir>/sessions/--<cwd>--/<timestamp>_<id>.jsonl`。
- `<cwd>` 去掉开头分隔符，把 `/`、`\`、`:` 替换成 `-`（与 pi 的约定一致）。
- 每行一个 JSON 对象，消息级记录：`user`、`assistant`、`tool`、`turn`。
- 同目录 `meta.json` 存模型、provider、cwd、开始时间、工具列表。
- 不存每个 token 的增量，避免文件膨胀；流式内容只保留最终 assistant 消息。
- 每次启动新建会话；恢复功能不在 MVP。

## 8. 代码结构

```text
cmd/sprout/main.go                    参数、配置、装配、启动 TUI/CLI
internal/agent/agent.go               ReAct 循环
internal/agent/message.go             Message / ToolCall / ToolResult
internal/agent/provider.go            Provider 接口
internal/agent/prompt.go              系统提示词
internal/agent/event/event.go         事件类型与 Sink
internal/agent/config/config.go       models.jsonc 解析与校验
internal/agent/config/paths.go        数据目录解析
internal/agent/config/template.go     配置模板
internal/agent/session/jsonl.go       JSONL 写入 + meta.json
internal/agent/tool/tool.go           工具接口与注册表
internal/agent/tool/shell/shell.go    执行、超时、截断
internal/agent/tool/bash/bash.go      Git Bash / Unix bash
internal/agent/tool/powershell/powershell.go
internal/agent/platform/shell_windows.go
internal/agent/platform/shell_unix.go
internal/agent/platform/console_windows.go
internal/agent/platform/console_other.go
internal/provider/openai/client.go    HTTP、鉴权、错误归一化
internal/provider/openai/chat.go      Chat Completions 适配
internal/provider/openai/responses.go Responses 适配
internal/provider/openai/types.go     两种协议的线格式
internal/provider/openai/sse.go       通用 SSE 读取
internal/tui/model.go                 Bubble Tea 主模型
internal/tui/update.go                消息处理
internal/tui/view.go                  布局与渲染
internal/tui/entries.go               transcript 条目与工具渲染
internal/tui/commands.go              斜杠命令 /tools /tool
internal/cli/run.go                   单次运行
models.example.jsonc                  示例配置
```

## 9. 实现步骤

### Step 1：TUI 骨架 + 真实 shell 工具

- 全屏 Bubble Tea 主模型、header、viewport、textarea、状态行。
- 安装级数据目录、配置加载、首次生成 `models.jsonc`。
- 真实 `bash` / `powershell` 检测、执行、超时、头尾截断；通过 `/tools`、`/tool` 手动调用。
- 假 agent 只做流式回显，不伪造工具结果。
- 验收：双击 `sprout.exe` 进入界面；`/tool powershell Get-Location` 返回真实输出；有 Git Bash 时 `/tool bash pwd` 可用。

### Step 2：接上两种 OpenAI 协议

- 2a：Chat Completions 非流式，跑通请求、响应、错误。
- 2b：Responses 非流式，复用同一套内部消息模型。
- TUI 显示最终回答；provider 由 `models.jsonc` 的 `protocol` 选择。
- 验收：同一句“你好”分别用 `openai-chat` 和 `openai-responses` 得到回答；401/网络错误显示在 transcript。

### Step 3：把工具接进 ReAct 循环

- 工具在 Step 1 已实现；本步把 Tool 定义喂给模型，解析 tool call，执行并回传结果。
- 验收：Windows 上“列出当前目录文件”走 powershell；装了 Git Bash 时可走 bash；卸载/隐藏 Git Bash 后 bash 从工具列表消失。

### Step 4：流式、会话与收尾

- SSE 流式、成功/失败路径、会话 JSONL、`sprout run`、错误提示。
- 验收：首 token 流式出现；会话文件完整；Ctrl+C 能取消；双击崩溃时界面不闪退。

## 10. 验收标准

1. `go build -o bin/sprout.exe ./cmd/sprout` 成功，双击能打开 TUI。
2. 首次运行在 `bin/data/.sprout/models.jsonc` 生成带注释的配置；已有文件不覆盖。
3. 输入消息后流式显示回答，生成期间可取消。
4. 切换 `models.jsonc` 的 `protocol`，Chat Completions 与 Responses 都能完成一轮对话和工具调用。
5. `/tool powershell Get-Location` 返回真实输出；有 Git Bash 时 `/tool bash pwd` 可用，无 Git Bash 时显示原因。
6. “看看当前目录有哪些文件”触发 shell 工具，命令、退出码、截断输出可见，最终回答正确。
7. Windows 同时检测 `powershell` 与 Git Bash 时两个工具都可用；只有 PowerShell 时 `bash` 不出现在工具列表。
8. PowerShell 5.1 下模型不会生成 `&&` 等 7+ 语法；7+ 下正常。
9. `models.jsonc` 支持注释和尾逗号，解析错误能定位到行列。
10. 会话 JSONL + `meta.json` 落在 `<dataDir>/sessions/` 下。
11. 缺少 API Key、网络失败、工具超时不 panic，错误显示在 TUI。
12. `go vet ./...` 通过；SSE 解析、tool call 拼接、shell 检测、配置解析有单元测试。

## 11. 明确不做

- WebUI、JSON-RPC、后台 daemon。
- Anthropic Messages。
- MCP、skills、文件读写/搜索工具。
- 权限审批、沙箱。
- 会话恢复、上下文压缩、多 agent。
- Markdown 渲染、鼠标、图片。

## 12. 参考与借鉴

| 来源 | 借鉴内容 |
| --- | --- |
| Claude Code `BashTool` / `PowerShellTool` | 两个 shell 工具并列；PowerShell 版本感知提示；Git Bash 缺失时给出检测路径 |
| Claude Code `src/utils/shell/powershellDetection.ts` | `pwsh` 优先、`powershell` 回退；按 edition 生成不同提示 |
| Claude Code `resolveDefaultShell.ts` | Windows 默认 shell 可配置，不写死 |
| pi `docs/windows.md` | Windows Bash 解析顺序；PowerShell 探测路径；`defaultTools` 选择机制 |
| pi `docs/session-format.md` | JSONL 会话、按 cwd 分目录、消息级记录 |
| Crush `internal/ui/AGENTS.md` | Bubble Tea 单模型；`Update` 不做 IO；工具渲染器 |
| OpenCode `packages/cli/src/config/config.ts` | 用户配置使用 JSONC，保留注释 |
| OpenAI 官方 Responses streaming 文档 | 流式事件名、函数调用参数 delta、`function_call_output` 回传格式 |
| Codex `app-server-client` | 协议优先、同进程运行的思路；MVP 先不做外部 server |
