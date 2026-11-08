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

// tool call 不走 Chunk，流结束后随 Response 返回；usage 也只在 Response 里。
type Chunk struct {
    TextDelta string
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
- 错误统一成 `ProviderError{Status, Code, Message, Retryable, Err}`，`Error()` 直接给出中文说明；`Status` 为 0 表示没有拿到 HTTP 响应（连接失败或流中途中断）。

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
// stdout 与 stderr 分开收集，回给模型时由 Result.ForModel() 格式化。
type Result struct {
    Command   string
    Stdout    string
    Stderr    string
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

参数统一 `{"command":"..."}`，可选 `timeout_seconds`（1–600，默认 120，越界取上限）。

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

- 路径：`<dataDir>/sessions/--<cwd slug>--/<yyyymmdd-hhmmss>_<id>.jsonl`。slug 去掉开头的分隔符，把 `/`、`\`、`:` 换成 `-`，如 `D:\SmyProjects\sprout` → `--D--SmyProjects-sprout--`。
- 记录类型（每行一条，`type` 区分）：`session`（头）、`user`、`assistant`（含 `tool_calls`）、`tool`（含完整 stdout/stderr、退出码、耗时）、`usage`、`turn`（`reason`）、`error`。
- 会话记录器是 `event.Sink`，与 surface 订阅同一条事件流；`message.delta` 和 `tool.requested` 不落盘。
- 文件在第一个可落盘事件到来时才创建；启动后什么都没做就退出不会留下空文件。权限 0600。
- 同目录 `meta.json` 是该工作目录下所有会话的索引：`{cwd, sessions: {<文件名>: {id, provider, model, tools, startedAt, version}}}`，原子替换写入，损坏时重建。不加跨进程锁，两个实例同时在同一目录启动可能丢一条登记，JSONL 本身不受影响。
- 写入失败只记日志，不影响 agent。
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

- [ ] `go build -o bin/sprout.exe ./cmd/sprout` 成功，双击进入全屏 TUI。（构建与命令行启动已验证；双击启动和真实终端里的全屏界面未手工验证）
- [x] 首次运行生成 `bin/data/.sprout/models.jsonc`；已有文件不覆盖。（真实二进制冒烟 + 单元测试）
- [ ] DeepSeek `openai-chat` 配置下，真实对话成功，流式显示。（协议层用 httptest 回放 SSE 验证；未用真实 Key）
- [ ] 切换 `openai-responses`，真实对话成功。（同上；`function_call` 不带 `id` 的做法依据文档推断，需真实服务验证）
- [ ] 输入“看一下当前目录，告诉我 Go 入口文件”，模型真实调用 powershell，TUI 显示命令、退出码、输出。（agent + 真实 PowerShell 进程 + 模型服务替身已走通 `sprout run`；TUI 渲染有测试；未用真实模型、未在真实 TUI 里手工操作）
- [x] 有 Git Bash 时，模型可调用 bash；没有时 `bash` 不出现在工具列表。（真实 Git Bash 进程测试 + 注册测试）
- [x] 工具输出超过 50KB 时截断；超时能取消；exit code 非 0 能回给模型。
- [ ] 生成中 Ctrl+C 取消；空闲 Ctrl+C/Ctrl+D 退出。（按键处理和取消链路用真实 `tea.Program` 验证；Windows 控制台里 Ctrl+C 是否按键事件送达未手工验证）
- [x] `bin/data/.sprout/sessions/...` 生成会话 JSONL 和 `meta.json`。
- [x] 缺 Key、401、网络失败、工具超时都有中文提示，不 panic。
- [x] `go test ./...`、`go vet ./...` 通过。（未用 `-race`：本机没有 gcc）
- [x] 代码里没有默认启用的假模型或假工具路径。（替身只出现在 `_test.go`）

## 13. 风险

1. **两种协议的 tool call 流式结构差异**：Chat 按 index 拼 delta；Responses 用 item/arguments 事件。分别写测试。
2. **PowerShell 5.1 编码**：重定向输出可能是 UTF-16LE/OEM；先设控制台 UTF-8，再实测中文。
3. **Windows 超时进程树**：已处理。`shell.Run` 通过 `platform.ConfigureCmd` 在超时/取消时终止整个进程树（Unix 用进程组，Windows 用 `taskkill /T /F`），并设置 `WaitDelay` 防止孙进程占着管道导致 Wait 卡住。未使用 Job Object。
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

## 15. 实现与计划的差异

以下是落地时有意偏离上文设计的地方，以代码为准。

**事件协议**（`internal/agent/event`）

- 新增 `message.completed`：一次模型调用结束后携带完整 assistant 消息（正文、tool calls、stop reason）。会话记录器只订阅事件流，不让 agent 依赖 session 包，所以完整消息必须出现在事件里。
- `tool.finished` 带完整 stdout/stderr（已受 50KB 上限约束）和 `Cancelled`、`Err` 字段。被取消而没来得及执行的调用只有 `tool.finished`，没有 `tool.requested`。
- 每轮必以一个 `turn.finished` 结束，`reason` 为 `stop` / `cancelled` / `error` / `max_steps` / `length`。`error` 事件之后一定跟着 `turn.finished`。
- 事件协议里没有单独的 `Command` 类型：取消通过 `context` 完成，提交通过 `Agent.Run(ctx, prompt, sink)`。

**内核语义**

- 最大轮数按“模型调用次数”计，默认 10；超过后发 `error` 并以 `max_steps` 结束。
- 取消或失败时为尚未执行的 tool call 补上“未执行”结果，保证历史里每个 assistant tool call 都有对应的 tool 消息，否则下一次请求会被服务端拒绝。
- 第一次模型调用就失败或被取消时撤销本轮用户消息，避免重试出现重复提问。
- 空的工具参数规范化为 `{}`。
- panic 被 recover，历史还原到本轮开始前，仍然以 `turn.finished(error)` 收尾。

**Provider**

- `Chunk` 只有 `TextDelta`；usage 只在 `Response` 里（原计划的 `Chunk.Usage` 没有用处，已删除）。
- Chat 请求带 `stream_options.include_usage`，usage 在 `finish_reason` 之后的独立 chunk 里，所以读到 `[DONE]` 或 EOF 才结束。
- Chat 同一个 `index` 出现不同的非空 `id` 时视为新调用（个别兼容服务给每个调用都填 0）。
- Responses 的函数调用来自 `added` / `arguments.delta` / `arguments.done` / `output_item.done` / `completed.output` 多个来源，后到的覆盖先到的；文本已通过增量输出过就不再用完整 item 重复输出。
- Responses 的 `function_call` 条目不带 `id`（`store:false` 时没有配对的 reasoning 条目）；这一点依据文档推断，**尚未用真实服务验证**。
- Responses 的函数工具显式 `strict:false`，否则默认 `strict:true` 会要求 schema 满足严格模式。
- 发送给服务端的输出上限直接取 `models.jsonc` 里的 `maxOutput`（Chat 为 `max_tokens`，Responses 为 `max_output_tokens`）。个别新模型（如 OpenAI 的 o 系列）在 Chat 协议下拒绝 `max_tokens`，这类模型请使用 Responses 协议。`contextWindow` 目前不参与任何逻辑（M1 没有上下文管理），仅作记录。

**工具**

- `Result` 把 stdout 与 stderr 分开保存；回给模型时由 `ForModel()` 统一格式化。
- 总输出上限 50KB，stdout 占 80%、stderr 占 20%，各自保留头尾。
- 工具参数增加可选的 `timeout_seconds`。
- PowerShell 在命令前注入 UTF-8 输出编码前导并关闭进度条（避免 stderr 里出现 CLIXML 噪声）；PowerShell 的退出码在 `-Command` 模式下失败时通常是 1，不一定等于原生程序自己的退出码，已写进工具描述。

**会话**

- `meta.json` 是工作目录分组下的会话索引（见 §10），而不是单个会话的元数据。

**TUI**

- 输入框用真实终端光标（输入法候选窗跟随），只有首行带 `>` 提示符。
- 退出时 `Model.Shutdown` 取消进行中的一轮并最多等 5 秒让 agent 收尾，保证最后的 `turn.finished(cancelled)` 能被会话记录。
- Windows 双击启动（独占控制台）出错退出前会等待回车，让用户看清错误。

**尚未验证**

- 真实 DeepSeek / OpenAI 服务（需要 Key）：两种协议只用 httptest 回放验证过。
- 真实终端里的 TUI 手工操作，尤其是中文输入法下 Enter 的组合期行为、Windows 控制台里 Ctrl+C 的送达方式。
- `go test -race`（本机没有 gcc）。
