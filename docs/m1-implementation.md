# M1 实现计划：最小可用 ReAct Agent

> 目标：真实可用的最小闭环。不是 UI 演示，不是假数据。
> 用户配置真实 API Key，双击 exe，在 TUI 里发一句话，模型真实推理、真实调用 bash/powershell、观察真实输出，最后给出答案。

## 1. 最小闭环的定义

一条完整链路：

```text
用户输入
  → 组装 system prompt + 历史消息 + 工具定义
  → 调用真实 LLM（OpenAI Chat Completions 或 Responses）
  → 流式文本 / tool call
  → 执行真实 bash 或 powershell
  → 把 stdout / stderr / exit code 回给模型
  → 继续推理，直到没有 tool call
  → TUI 显示最终答案
  → 会话写入 JSONL
```

缺任何一环都不算完成 M1。

## 2. 验收场景

1. 把 DeepSeek 或 OpenAI 的 Key 填进 `bin/data/.sprout/models.jsonc`。
2. 双击 `bin/sprout.exe`，进入全屏 TUI。
3. 输入：

   > 看一下当前目录，告诉我 Go 入口文件是哪个，并解释你怎么找到的。

4. 模型发起 `powershell`（或 `bash`）工具调用。
5. TUI 显示真实命令、退出码、输出预览。
6. 模型基于真实输出继续推理，给出最终答案。
7. `bin/data/.sprout/sessions/...` 下生成本次会话的 JSONL。
8. 把 `default` 换成 `openai-responses` 的模型，重复以上流程，仍然成功。

## 3. 范围

| 包含 | 不包含 |
| --- | --- |
| 全屏 Bubble Tea TUI | WebUI、JSON-RPC、daemon |
| 真实 OpenAI Chat Completions | Anthropic Messages |
| 真实 OpenAI Responses | 多模型自动路由 |
| 真实 ReAct 循环 | 多 agent 编排 |
| 真实 `bash` / `powershell` | 文件读写、搜索、web、MCP、skills |
| `models.jsonc` + 安装级数据目录 | 配置热加载、GUI 设置 |
| JSONL 会话 | 会话恢复、分支、压缩 |
| `sprout run "..."` 单次 CLI | 交互式 CLI REPL |
| Windows 控制台 UTF-8、双击运行 | 权限审批、沙箱 |

## 4. 数据目录

默认：

```text
bin/
├── sprout.exe
└── data/
    └── .sprout/
        ├── models.jsonc
        ├── sessions/
        └── logs/
```

- 安装级 / 系统级，跟随二进制，不跟随用户 home，也不跟随项目。
- 解析顺序：`--data-dir` > `SPROUT_DATA_DIR` > `<exeDir>/data/.sprout`。
- `os.Executable()` 定位；目录不可写时报错并提示 `--data-dir`，不静默回退到 `~/.sprout`。
- `go run` 时显式传 `--data-dir ./data/.sprout`。

## 5. 配置

用户配置一律 JSONC，路径 `<dataDir>/models.jsonc`；内部 `meta.json`、会话 JSONL 用普通 JSON/JSONL。

```jsonc
{
  "default": "deepseek/deepseek-chat",

  "providers": {
    "deepseek": {
      "protocol": "openai-chat",
      "baseUrl": "https://api.deepseek.com/v1",
      // 优先读环境变量；也可以直接写 apiKey
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
    // 留空自动探测；Windows 可填 Git Bash / PowerShell 绝对路径
    "bashPath": "",
    "powershellPath": ""
  }
}
```

校验：

- `default` 必须是 `provider/model`，两边都存在。
- `protocol` 只允许 `openai-chat`、`openai-responses`。
- `baseUrl` 必须 http/https。
- `apiKeyEnv` 优先，`apiKey` 兜底；都没有时 TUI 显示配置提示，不发起请求。
- 首次运行生成模板，不覆盖已有文件。

## 6. Provider

### 内部模型

```go
type Message struct {
    Role      string        // system / user / assistant / tool
    Content   string
    ToolCalls []ToolCall
    ToolID    string
}

type ToolCall struct {
    ID        string
    Name      string
    Arguments string // 原始 JSON 字符串
}

type ToolSpec struct {
    Name        string
    Description string
    Parameters  json.RawMessage
}

type Request struct {
    Model           string
    System          string
    Messages        []Message
    Tools           []ToolSpec
    MaxOutputTokens int
}

type Chunk struct {
    TextDelta string
    Usage     *Usage
}

type Response struct {
    Message    Message
    Usage      Usage
    StopReason string
}

type Provider interface {
    Stream(ctx context.Context, req Request, emit func(Chunk) error) (Response, error)
}
```

### Chat Completions 适配

- `POST {baseUrl}/chat/completions`，`stream:true`。
- system 放 `messages[0]`。
- 工具声明：`tools[].function.{name,description,parameters}`。
- 流式文本：`choices[0].delta.content`。
- 工具调用：按 `index` 累积 `delta.tool_calls[]` 的 `id`、`function.name`、`function.arguments`。
- 工具结果：`{"role":"tool","tool_call_id":"...","content":"..."}`。
- 结束：`finish_reason` 为 `stop` 或 `tool_calls`。
- 有 tool call 时不要求 `finish_reason` 一定是 `tool_calls`，以实际累积结果为准。

### Responses 适配

- `POST {baseUrl}/responses`，`stream:true`，`store:false`。
- system 放 `instructions`。
- 历史放 `input[]`；assistant 的 function call 作为 `function_call` item 回传。
- 工具声明是扁平结构：`{type:"function", name, description, parameters}`。
- 文本流：`response.output_text.delta`。
- 函数调用：`response.output_item.added` + `response.function_call_arguments.delta` / `done`，或 `response.output_item.done` 的完整 item。
- 工具结果：`{type:"function_call_output", call_id, output}`。
- 结束：`response.completed`；错误看 `response.failed`。
- usage 从 `response.completed.response.usage` 取。

### 归一化要求

- agent 只处理内部 `Message` / `ToolCall`，不出现协议字段。
- 两个适配器都只向外发 `message.delta`；tool call 在流结束后交给 agent 循环。
- 错误统一成 `ProviderError{Status, Code, Message, Retryable}`，TUI 显示可读中文。

## 7. ReAct 循环

```text
history = [system, user]

for step in 0..10:
    resp = provider.Stream(history, tools)
    history += resp.Message

    if resp.Message.ToolCalls 为空:
        break

    for call in resp.Message.ToolCalls:
        emit tool.requested(call)
        result = tool.Run(call)
        emit tool.finished(result)
        history += toolResultMessage(call, result)

if step == 10 且仍有 tool call:
    emit error("超过最大工具调用轮数")
```

规则：

- 工具串行执行，MVP 不做并行。
- 每个 tool result 必须包含命令、退出码、stdout、stderr、是否截断。
- 工具返回 error（进程起不来）也转成 tool result，让模型知道失败原因。
- 支持 `context` 取消；取消时停止 HTTP 请求和正在执行的工具。
- 不压缩上下文；超出模型窗口时显示明确错误，保留会话文件。

## 8. 工具

### 接口

```go
type Result struct {
    Command   string
    Output    string
    ExitCode  int
    Truncated bool
    TimedOut  bool
    Duration  time.Duration
}

type Tool interface {
    Name() string
    Description() string
    Parameters() json.RawMessage
    Run(ctx context.Context, args json.RawMessage) (Result, error)
}
```

参数统一 `{"command":"..."}`。

### 检测

**PowerShell（仅 Windows）**：

1. `shell.powershellPath`
2. `pwsh.exe` on PATH
3. `%ProgramFiles%\PowerShell\7\pwsh.exe`
4. `powershell.exe` on PATH
5. `%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe`

记录 edition：`pwsh` = 7+，`powershell` = 5.1。

**Bash**：

1. `shell.bashPath`
2. `%ProgramFiles%\Git\bin\bash.exe`
3. `%ProgramFiles(x86)%\Git\bin\bash.exe`
4. PATH 里的 `bash.exe`（MSYS2 / Cygwin）
5. Windows 上只有 `System32\bash.exe`（WSL 启动器）时判定不可用，提示安装 Git for Windows
6. Linux / macOS / Termux：PATH 里的 `bash`

### 执行

- `bash --noprofile --norc -c <command>`。
- `powershell -NoProfile -NonInteractive -Command <command>`。
- 工作目录固定为启动目录；每次新进程，`cd` 不跨调用保持。
- 默认超时 120 秒，最大 600 秒。
- 输出 50KB 上限，保留头尾并标注截断。
- stdout / stderr 分开收集。
- 不接管 stdin，不支持后台任务。
- PowerShell 5.1 先设置控制台 UTF-8；中文输出作为重点测试项。

### 工具描述

直接进入模型上下文：

- `bash`：Windows 上是 Git Bash，路径 `/d/...`；每次新进程；不要交互式命令。
- `powershell`：标明 edition；5.1 不支持 `&&` / `||` / `?:` / `??`；不要对原生程序用 `2>&1`；每次新进程。

## 9. TUI

- 全屏 Bubble Tea v2 + lipgloss + bubbles。
- Header：模型、cwd、数据目录、可用工具。
- Transcript：viewport，流式文本、工具卡片、错误。
- Status：spinner / 当前工具 / 快捷键。
- Input：textarea，Enter 发送，Ctrl+J 换行，Ctrl+C 取消/退出，Ctrl+D 退出。
- `/tools` 查看检测结果；`/help`；`/quit`。
- 工具卡片：命令、退出码、耗时、输出预览（最多 8 行，截断标注）。
- 不做 Markdown、鼠标、代码高亮。

事件流：

```text
TUI 输入 → agent.Run → event channel → tea.Cmd → TUI 更新
```

`Update` 不做 IO；agent 在 goroutine 里跑；取消用 context。

## 10. 会话

- 路径：`<dataDir>/sessions/--<cwd>--/<timestamp>_<id>.jsonl`。
- 消息级记录：user、assistant（含 tool calls）、tool result、turn。
- 同目录 `meta.json`：provider、model、cwd、工具列表、开始时间。
- 不存 token delta；只存最终 assistant 消息。
- M1 不做恢复；启动新会话。

## 11. 实现检查点

三个检查点都是真实功能，没有假 agent。

### A. 真实对话

- 数据目录、配置、Provider 接口、Chat Completions + Responses 流式。
- TUI 全屏、输入、流式输出、取消。
- 验收：真实 Key，输入“你好”得到真实流式回答；切换 protocol 仍可用。
- 注意：A 只是构建中间点，不是 M1 交付。

### B. 最小闭环

- Tool 接口/Registry、bash/powershell 检测与执行、系统提示、工具 schema 下发、tool call 解析、结果回传、最多 10 轮。
- 验收：第 2 节的完整场景；这是 M1 的最小可用点。

### C. M1 完成

- 会话 JSONL、`sprout run`、Windows 控制台 UTF-8、双击错误暂停、日志。
- 验收：会话文件完整，单次 CLI 可用，重启后数据仍在。

## 12. 验收清单

- [ ] `go build -o bin/sprout.exe ./cmd/sprout` 成功，双击进入全屏 TUI。
- [ ] 首次运行生成 `bin/data/.sprout/models.jsonc`；已有文件不覆盖。
- [ ] DeepSeek `openai-chat` 配置下，真实对话成功，流式显示。
- [ ] 切换 `openai-responses`，真实对话成功。
- [ ] 输入“看一下当前目录，告诉我 Go 入口文件”，模型真实调用 powershell，TUI 显示命令、退出码、输出。
- [ ] 有 Git Bash 时，模型可调用 bash；没有时 `bash` 不出现在工具列表。
- [ ] 工具输出超过 50KB 时截断；超时能取消；exit code 非 0 能回给模型。
- [ ] 生成中 Ctrl+C 取消；空闲 Ctrl+C/Ctrl+D 退出。
- [ ] `bin/data/.sprout/sessions/...` 生成会话 JSONL 和 `meta.json`。
- [ ] 缺 Key、401、网络失败、工具超时都有中文提示，不 panic。
- [ ] `go test ./...`、`go vet ./...` 通过。
- [ ] 代码里没有默认启用的假模型或假工具路径。

## 13. 风险

1. **两种协议的 tool call 流式结构差异**：Chat 按 index 拼 delta；Responses 用 item/arguments 事件。分别写测试。
2. **PowerShell 5.1 编码**：重定向输出可能是 UTF-16LE/OEM；先设控制台 UTF-8，再实测中文。
3. **Windows 超时进程树**：`CommandContext` 只杀直接子进程；M1 记录限制，后续用 Job Object。
4. **双击 cwd**：数据目录用 exe 路径，不依赖 cwd；会话分组后续再处理。
5. **Bubble Tea v2 API**：模块路径 `charm.land/...`，`View() tea.View`，`tea.KeyPressMsg`；按 Crush 源码写并锁版本。
6. **输入法 Enter**：必须手工测中文输入，避免组合期误发送。
7. **数据目录不可写**：Program Files 下会失败；报错并提示 `--data-dir`。
8. **上下文超限**：M1 不压缩，直接报错；不静默截断历史。
9. **工具输出污染上下文**：50KB 截断必须生效，否则一次构建日志就能撑爆窗口。

## 14. 明确不做

- 假 agent、假工具、演示模式。
- WebUI、JSON-RPC、daemon。
- Anthropic Messages。
- MCP、skills、文件读写/搜索工具。
- 权限审批、沙箱。
- 会话恢复、上下文压缩、多 agent。
- Markdown、鼠标、图片。
