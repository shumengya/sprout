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
| JSONL 会话 | 会话分支、搜索 |
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
  "default": "deepseek/deepseek-flash",

  "providers": {
    "deepseek": {
      "protocol": "openai-chat",
      "baseUrl": "https://api.deepseek.com",
      // 优先读环境变量；也可以直接写 apiKey
      "apiKeyEnv": "DEEPSEEK_API_KEY",
      "apiKey": "",
      "models": {
        "deepseek-flash": {
          "contextWindow": 1000000,
          "maxOutput": 384000,
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
- `protocol` 只允许 `openai-chat`、`openai-responses`、`anthropic-messages`、`google-generate`。
- `baseUrl` 必须 http/https。
- `apiKeyEnv` 优先，`apiKey` 兜底；都没有时 TUI 显示配置提示，不发起请求。
- 首次运行生成模板，不覆盖已有文件。

## 6. Provider

### 内部模型

```go
// 内容是一串块，纯文本消息只有一个 text 块；带图片的消息按原顺序排列，图文交错不会丢
// （M2 起，见 docs/m2-vision.md）。
type Message struct {
    Role      string        // system / user / assistant / tool
    Content   []Block
    Reasoning string
    ToolCalls []ToolCall
    ToolID    string
}

type Block struct {
    Type      string // text | image
    Text      string
    MediaType string // image 专用，如 image/png
    Data      string // image 专用，base64
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
- 工具结果：`{"role":"tool","tool_call_id":"...","content":"..."}`；`content` 始终是字符串。
- 图片：user 消息的 `content` 从字符串改成 parts 数组，文本 `{"type":"text","text":...}`、
  图片 `{"type":"image_url","image_url":{"url":"data:<mime>;base64,..."}}`。纯文本消息仍发字符串，兼容性最好。
- 工具结果的图片（Chat 的 tool 消息只能带文本）：把图片攒到这组 tool 消息之后，用一条 user 消息补发。
- 结束：`finish_reason` 为 `stop` 或 `tool_calls`。
- 有 tool call 时不要求 `finish_reason` 一定是 `tool_calls`，以实际累积结果为准。

### Responses 适配

- `POST {baseUrl}/responses`，`stream:true`，`store:false`。
- system 放 `instructions`。
- 历史放 `input[]`；assistant 的 function call 作为 `function_call` item 回传。
- 工具声明是扁平结构：`{type:"function", name, description, parameters}`。
- 文本流：`response.output_text.delta`。
- 函数调用：`response.output_item.added` + `response.function_call_arguments.delta` / `done`，或 `response.output_item.done` 的完整 item。
- 工具结果：`{type:"function_call_output", call_id, output}`；`output` 始终是字符串，工具结果的图片同样在其后用 user 消息补发。
- 图片：user 消息 `content` 用 `input_text` / `input_image`（data URL）数组。
- 结束：`response.completed`；错误看 `response.failed`。
- usage 从 `response.completed.response.usage` 取。

### Anthropic Messages 适配（`internal/provider/anthropic`）

- `POST {baseUrl}/messages`，`stream:true`；鉴权头 `x-api-key` + `anthropic-version: 2023-06-01`，不发 Bearer。
- system 放顶层 `system` 字段，不进 `messages`。
- `max_tokens` 必填：模型未配 `maxOutput` 时用默认值 4096。
- 工具声明：`tools[].{name,description,input_schema}`。
- assistant 历史：文本与 `tool_use` block 放在同一条 assistant 消息；不回传 Reasoning 文本（thinking block 需服务端签名，且历史可能来自其他协议）。
  开启思考后，同一模型产生的带思考块的消息改为原样回传 `Message.Native`，见 `docs/effort.md` §4。
- 工具结果：`tool_result` block 放进 user 消息，连续多条合并为一条；找不到配对 `tool_use` 的孤儿结果降级为 text block。
- 图片：`{"type":"image","source":{"type":"base64","media_type":...,"data":...}}`；工具结果带图片时
  `tool_result.content` 从字符串改为 text / image block 数组。
- 流式事件：`message_start`（input usage）→ `content_block_start/delta/stop`（`text_delta`、`thinking_delta`、`input_json_delta`）→ `message_delta`（stop_reason、output usage）→ `message_stop`。
- 停止原因：`tool_use` → tool_calls，`max_tokens` → length，其余 → stop。没有 `message_stop` 视为流被截断，可重试。
- 错误：HTTP 错误体与流内 `error` 事件都是 `{type:"error", error:{type,message}}`；529 overloaded 可重试，`prompt is too long` 识别为上下文超长。

### Google Gemini 适配（`internal/provider/google`）

- `POST {baseUrl}/models/{model}:streamGenerateContent?alt=sse`；鉴权头 `x-goog-api-key`。模型名带 `models/` 前缀时自动去掉。
- system 放 `systemInstruction.parts[]`；`maxOutput` 为 0 时不发 `generationConfig`。
- 角色映射：assistant → `model`；工具结果作为 user 消息里的 `functionResponse` part，连续多条合并。
- `functionResponse` 按 `name` 配对，name 从历史里的调用 id 反查；`response` 必须是对象，包成 `{"result": 输出}`。
- 图片：part 用 `{"inlineData":{"mimeType":...,"data":...}}`。`functionResponse.response` 只能放 JSON 对象，
  工具结果的图片放不进去：结果文本里注明附了图片，图片本身在同一组 functionResponse 之后用 user 消息补发。
- Gemini 不返回调用 id，本地生成 `call_<随机>_<函数名>`。参数缺省时补 `{}`。
- 流式：每个 SSE data 是完整 `GenerateContentResponse`；`thought:true` 的 text part 归为 Reasoning。
- 结束：没有显式结束事件，以出现 `finishReason` 为准；有 functionCall 时停止原因为 tool_calls，`MAX_TOKENS` → length。流结束仍无 finishReason 视为截断，可重试。
- `promptFeedback.blockReason` 报 `content_blocked` 错误；流内 `{error:{code,message,status}}` 与 HTTP 错误体同结构。

### 共享传输层（`internal/provider/transport`）

- SSE 读取、默认 HTTP 客户端、HTTP 错误归一化、`Retry-After` 解析、中断判定由四个适配器共用；各协议只负责自己的请求/响应结构，不做跨协议字符串拼接。

### 归一化要求

- agent 只处理内部 `Message` / `ToolCall`，不出现协议字段。
- 所有适配器都只向外发 `message.delta`；tool call 在流结束后交给 agent 循环。
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

- 同一批工具调用并发执行（见 §15）；事件发送和历史写入始终在 agent 主 goroutine。
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
- 底部布局紧凑（参考 Claude Code）：从上到下是可选的活动行、分隔线、输入框、分隔线、信息栏，空闲时只占 4 行。
- 信息栏（最底部一行）：左边模型、当前目录、可用工具，中文标签、不同颜色；右端是本会话累计 token。宽度不够时 token 数先让位，其次目录从左边截断。数据目录不在此显示，用 `/tools` 查看。
- 活动行（输入框上方）：只在生成中或有临时提示（正在取消、已复制等）时出现，内容是 spinner、当前在做什么（生成中 / 思考中 / 执行某工具 / 并行执行 N 个工具）、超过 3 秒后的耗时，右端是“Ctrl+C 取消”。空闲时整行消失，不再显示“空闲”。
- Input：textarea，提示符 `❯`，按内容增高（空时一行，最多显示 8 行，再多在框内滚动，内容不受限），发送后回到一行。Enter 发送，Ctrl+J 换行，Ctrl+C 取消/退出，Ctrl+D 退出。活动行出现、输入框增高时对话区相应变矮，跟随底部时保持贴底。
- Transcript：viewport，流式文本、工具卡片、错误。
- Status：见上面的活动行。
- `/tools` 查看检测结果；`/image 路径` 附加图片到下一条消息（`/image` 查看、`/image clear` 清空）；`/new` 开新会话；`/resume` 打开会话浏览器（替换输入框：Tab 切换当前目录 / 全部，输入即搜索，`re:` 正则、`"短语"` 精确，Ctrl+S 排序、Ctrl+N 只看已命名、Ctrl+R 重命名、Ctrl+D 删除、Ctrl+P 显示路径，Enter 恢复；`/resume 选择器` 跳过浏览器直接恢复）；`/help`；`/quit`。
- 工具卡片：命令、退出码、耗时、输出预览（最多 8 行，截断标注）。
- 不做代码高亮。（Markdown、代码高亮与鼠标滚轮是后补的，见 §15。）

事件流：

```text
TUI 输入 → agent.Run → event channel → tea.Cmd → TUI 更新
```

`Update` 不做 IO；agent 在 goroutine 里跑；取消用 context。

## 10. 会话

- 路径：`<dataDir>/sessions/--<cwd slug>--/<yyyymmdd-hhmmss>_<id>.jsonl`。slug 去掉开头的分隔符，把 `/`、`\`、`:` 换成 `-`，如 `D:\SmyProjects\sprout` → `--D--SmyProjects-sprout--`。
- 记录类型（每行一条，`type` 区分）：`session`（头）、`user`、`assistant`（含 `tool_calls`）、`tool`（含完整 stdout/stderr、退出码、耗时）、`usage`、`turn`（`reason`）、`error`。
- 带图片的 `user` 记录多一个 `images: [{file, media_type, bytes}]`，`file` 是相对会话文件的路径；
  图片文件存在 `<会话名>.images/` 目录里，按内容哈希命名（同一张图重复发只存一份）。JSONL 里不内联 base64。
- 会话记录器是 `event.Sink`，与 surface 订阅同一条事件流；`message.delta`、`reasoning.delta` 和 `tool.requested` 不落盘；`assistant` 记录带完整 `reasoning`（有思维链时），
  以及协议原始内容 `native`（目前只有开启思考的 Anthropic 消息会有，见 `docs/effort.md` §5）。
- 文件在第一个可落盘事件到来时才创建；启动后什么都没做就退出不会留下空文件。权限 0600。
- 同目录 `meta.json` 是该工作目录下所有会话的索引：`{cwd, sessions: {<文件名>: {id, provider, model, tools, startedAt, version, name?}}}`，原子替换写入，损坏时重建。`name` 是用户在会话浏览器里起的名字（只在当前目录的索引里读写；`/resume` 切到“全部目录”时按文件所在分组的 `meta.json` 逐条取，没有名字就显示第一条提问）。不加跨进程锁，两个实例同时在同一目录启动可能丢一条登记，JSONL 本身不受影响。
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

- Tool 接口/Registry、bash/powershell 检测与执行、系统提示、工具 schema 下发、tool call 解析、结果回传。
- 验收：第 2 节的完整场景；这是 M1 的最小可用点。

### C. M1 完成

- 会话 JSONL、`sprout run`、Windows 控制台 UTF-8、双击错误暂停、日志。
- 验收：会话文件完整，单次 CLI 可用，重启后数据仍在。

## 12. 验收清单

- [x] `go build -o bin/sprout.exe ./cmd/sprout` 成功，双击进入全屏 TUI。
- [x] 首次运行生成 `bin/data/.sprout/models.jsonc`；已有文件不覆盖。
- [x] DeepSeek `openai-chat` 配置下，真实对话成功，流式显示。
- [x] 切换 `openai-responses`，真实对话成功。（OpenAI 官方 Responses 服务未验证）
- [x] 输入”看一下当前目录，告诉我 Go 入口文件”，模型真实调用 powershell，TUI 显示命令、退出码、输出。
- [x] 有 Git Bash 时，模型可调用 bash；没有时 `bash` 不出现在工具列表。
- [x] 工具输出超过 50KB 时截断；超时能取消；exit code 非 0 能回给模型。
- [x] 生成中 Ctrl+C 取消；空闲 Ctrl+C/Ctrl+D 退出。
- [x] `bin/data/.sprout/sessions/...` 生成会话 JSONL 和 `meta.json`。
- [x] 缺 Key、401、网络失败、工具超时都有中文提示，不 panic。
- [x] `go test ./...`、`go vet ./...` 通过。（未用 `-race`：本机没有 gcc）
- [x] 代码里没有默认启用的假模型或假工具路径。（替身只出现在 `_test.go`）

**M1 已全部完成**（2026-11-11 用户确认手工测试通过）

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
- 多 agent。（会话恢复与上下文压缩是后补的，见 §15。）
- 图片、完整的 CommonMark 渲染、代码高亮。

## 15. 实现与计划的差异

以下是落地时有意偏离上文设计的地方，以代码为准。

**事件协议**（`internal/agent/event`）

- 事件新增 `interrupted`（`Reason` 为 `cancelled` / `error`，带写进历史的完整文本、思维链和 `Partial` 标志）：中断的那一轮保留在历史里，界面按 `Partial` 决定是补一行说明还是显示整条消息。`content` 不写入 `assistant` 记录，而是单独的 `interrupted` 记录，恢复时还原成同样的 assistant 消息。
- `tool.finished` 带完整 stdout/stderr（已受 50KB 上限约束）和 `Cancelled`、`Err` 字段。被取消而没来得及执行的调用只有 `tool.finished`，没有 `tool.requested`。
- 每轮必以一个 `turn.finished` 结束，`reason` 为 `stop` / `cancelled` / `error` / `max_steps` / `length`。`error` 事件之后一定跟着 `turn.finished`。
- 事件协议里没有单独的 `Command` 类型：取消通过 `context` 完成，提交通过 `Agent.Run(ctx, Input{Text, Images}, sink)`。

**内核语义**

- 轮数默认不设上限：任务做完为止，由用户取消（Ctrl+C）或模型自己停止结束。`Config.MaxSteps > 0` 时才限制，超过后发 `error` 并以 `max_steps` 结束（早期版本默认 10 轮，真实任务里常在做到一半时被掐断）。
- 取消或失败时为尚未执行的 tool call 补上“未执行”结果，保证历史里每个 assistant tool call 都有对应的 tool 消息，否则下一次请求会被服务端拒绝。
- 被取消或中途失败的轮次保留在历史里：已经流式收到的正文与思维链，连同一条中断说明（`[对话中断：用户取消]` / `[对话中断：<错误原文>]`）合成一条 assistant 消息追加进去，同时发出 `interrupted` 事件（会话记录按 `interrupted` 落盘，恢复时还原成同样的消息）。用户已经看到的内容，模型下一轮也该看到，否则说“继续”时它既不知道上一条问的是什么，也不知道自己说到一半被打断了。半截的 tool call 参数可能不完整，整个丢弃；半截内容与说明在同一条消息里，不会出现两条连续的 user 消息。
- 恢复旧会话时仍按当年的规则重放：旧记录里首次调用失败/被取消的轮次只留下了 user 消息，重放时撤销它（那时运行时确实撤销了）。新记录有 `interrupted` 记录，历史长度是 base+2，不会触发这条规则。
- 空的工具参数规范化为 `{}`。
- panic 被 recover，历史还原到本轮开始前，仍然以 `turn.finished(error)` 收尾。

**Provider**

- `Chunk` 只有 `TextDelta`；usage 只在 `Response` 里（原计划的 `Chunk.Usage` 没有用处，已删除）。
- Chat 请求带 `stream_options.include_usage`，usage 在 `finish_reason` 之后的独立 chunk 里，所以读到 `[DONE]` 或 EOF 才结束。
- Chat 同一个 `index` 出现不同的非空 `id` 时视为新调用（个别兼容服务给每个调用都填 0）。
- Responses 的函数调用来自 `added` / `arguments.delta` / `arguments.done` / `output_item.done` / `completed.output` 多个来源，后到的覆盖先到的；文本已通过增量输出过就不再用完整 item 重复输出。
- Responses 的 `function_call` 条目不带 `id`（`store:false` 时没有配对的 reasoning 条目）；依据文档推断。已用真实 DeepSeek 的 `/responses` 端点验证往返可用，但 **OpenAI 官方服务（尤其带 reasoning 的 gpt-5 / o 系列）尚未验证**。
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

**M1 落地后补充的 TUI 能力**

- 查看历史：鼠标滚轮（每格 3 行）、PgUp/PgDn、Ctrl+↑/↓ 逐行、Ctrl+Home/End 跳到最前/最后；输入框为空时 ↑ ↓ Home End 同样滚动对话。上滚会暂停自动跟随，对话区最后一行右端叠加显示“历史 N%，End 回到底部”（不另占一行：占一行的话上滚时对话区变矮、底部位置跟着变，滚回去会差一行），暂停期间有新内容到达时追加“有新内容”。
- `/mouse` 开关鼠标上报。开启时滚轮可用，且可以在对话区拖选复制：终端自带的拖选在鼠标上报开启后失效，所以选区由程序自己做（`select.go`、`select_view.go`）。按住左键拖动选中并反色显示，松开自动复制；选区用内容坐标记录，滚动时跟着内容走，拖到对话区上下边缘每个事件滚一行，所以能选中超过一屏的内容。选区两端各包含光标所在的字符，中文按整个字选中（点在字的任意一半都一样）。复制的是去掉样式、去掉行尾空白的纯文本，行首缩进保留。复制同时走 OSC 52（SSH 里也能用）和系统剪贴板（`atotto/clipboard`），两条路都不保证成功且无法互相确认，所以任何一条出错都只在状态行提示，不当作致命。内容重排使选区对不上时（窗口缩放、`/think` 展开、恢复会话）自动取消选区，否则高亮和复制的会是错位的文字；流式输出只往末尾追加，不影响靠前的选区。按任意键、点击对话区之外或右键也会取消选区。关闭后（`/mouse`）可直接用终端自带的选中；开启时终端自带的选中需要按住 Shift。
- 助手回答按 Markdown 渲染（`internal/tui/markdown*.go`）：goldmark 按 CommonMark + GFM 解析成语法树，再自己画成终端文本，不依赖 glamour。
  - 块级：标题（不显示 `#`，一级带下划线、一二级洋红、三级及以下黄色）、嵌套列表（圆点按层级轮换 `•` `◦` `▪`，序号右对齐）、任务列表 `[x]` / `[ ]`、多级引用（黄色 `│` 条，正文斜体）、分隔线、围栏与缩进代码块、HTML 块（注释隐藏，其余原样）。
  - 行内：粗体、斜体、删除线、行内代码、链接（文字后附淡色地址，文字本身就是地址时不重复）、裸链接、图片（`[图片: alt] (地址)`）、`<br>` 换行；其他 HTML 标签原样显示。软换行保留为换行。反斜杠转义与字符实体会解码，解码结果再过一遍 `sanitize`，防止 `&#27;` 注入终端控制序列。
  - 表格：带完整边框（┌┬┐├┼┤└┴┘），按显示宽度对齐（中文占两列），遵守 `:---:` 列对齐，单元格里的行内标记会渲染；有单元格折成多行时每条记录之间画分隔线，否则只在表头下面画一条；总宽超过终端时从最宽的列开始缩窄并在单元格内折行，每列可用宽度不足 4 列时改成逐行的“列名 内容”。
  - 代码块：chroma 切词，颜色映射到 ANSI 基础色（跟随终端主题，不设背景色）；没写语言或语言不认识时整体用绿色。只缩进两格、不画边框，选中复制时不带多余字符。按（语言，代码）缓存高亮结果，上限 64 项，淘汰最久未用的。
  - 样式自成一体：折行可能把一个样式区间切成两半，`mdSeal` 让每一行自带样式的开和关，viewport 从任意一行开始显示都不会串色。
  - 流式输出时每次增量都整段重渲，goldmark 对没写完的语法有明确规则（未闭合的围栏延伸到文末，未闭合的 `**` 与反引号原样保留），所以不吞字符；可能出现的闪动只有“表格表头在分隔行到达前显示成普通文字”和“`-` 在下一行到达前被当作 setext 标题线”。
  - 渲染器自己 panic 时 `renderMarkdown` 退回纯文本折行，界面不会崩。
  - 代价：新增依赖 goldmark 与 chroma（纯 Go，无 CGO）；Windows 上二进制约增加 4.8 MB（14.0 → 18.9 MB），`--help` 启动约多 17 ms（57 → 74 ms）。
- 模型的思维链用淡色单独显示：流式时只预览最后 3 行并显示字数；结束后折叠成一行“已思考 12秒 340 token：开头内容…”，思考内容压成一行接在后面，放不下的用省略号截断。点击条目展开全文（头部单独一行，正文缩进两格），再点一次收起；点击只切换被点的那一条，`/think` 一次展开或折叠全部，并决定之后新思考条目的默认状态。切换时被点中的条目头部停在屏幕原位，不会跳走；在长思考的正文中间点击收起时，头部移到对话区顶部。屏幕上有选区时，点击只取消选区、不切换；拖动仍是选中复制。耗时从第一段思维链增量到达算到思考结束（正文开始、工具调用或本轮结束），非流式服务和恢复的会话没有这个时刻，不显示耗时。token 数取服务端报告的思维链 token（见下），没报告时显示内核的估算并标“约”（`message.completed` 的 `ReasoningTokensEst`，规则见 `internal/agent/tokens.go`；思考到一半被取消时没有估算，不显示 token 数）。token 数一律缩写：950、2.9k、12k、1.5M（信息栏、思考条目、压缩提示都一样）。活动行显示“思考中”，超过 3 秒后追加已耗时。
- 空对话时显示一行新手引导（发送、翻页、`/help`）。
- `/install-sprout` / `/uninstall-sprout` 注册和卸载全局 `sprout` 命令（`internal/agent/platform/install*.go`，TUI 经 `Options.Installer` 注入，在 `tea.Cmd` 里执行）。注册的是当前运行的程序，原地调用、不复制：数据目录跟随 exe，复制到别处就换了一个数据目录。`go run` 的临时程序拒绝注册。
  - Windows：在 `%LOCALAPPDATA%\sprout\bin` 写 `sprout.cmd`（纯 ASCII、CRLF、无 BOM，带标记行；路径含非 ASCII 时改用 8.3 短路径，没有短路径就拒绝），把这个目录放到用户 PATH 最前面（已有的去重），并移除用户 PATH 里只装着旧 sprout 的目录（目录里的文件不动；目录里还有别的可执行文件时不移除，只提示）。用户 PATH 的其他条目原样保留，包括未展开的 `%VAR%`，注册表值类型（`REG_SZ` / `REG_EXPAND_SZ`）保持不变。每次改 PATH 前把原值备份到 `%LOCALAPPDATA%\sprout\path-backup-<时间>.txt`，改完广播 `WM_SETTINGCHANGE`，新开的终端生效。系统 PATH 只读：里面有别的 sprout 时提示需要管理员手动处理（Windows 先查系统 PATH）。先做完所有检查再写入；不是我们生成的 `sprout.cmd` 不覆盖、不删除。卸载删掉 `sprout.cmd`（目录空了一并删除）并从用户 PATH 移除那一条，不恢复安装时移除的旧条目。
  - Linux / macOS：`~/.local/bin/sprout` 符号链接；Termux：`$PREFIX/bin/sprout`。已有的符号链接替换并报告原来指向哪里，普通文件不覆盖。不改 shell 配置文件：目录不在 PATH 里时提示要加的那一行，PATH 里更靠前的另一个 sprout 只提示。卸载只删符号链接。
  - 已知限制：Windows 上经 `.cmd` 启动后，`sprout run` 运行中按 Ctrl+C 退出时 cmd 可能多问一句“终止批处理操作吗(Y/N)?”；TUI 不受影响（Ctrl+C 是界面里的按键）。

**思维链（reasoning_content）**

- `agent.Message` 增加 `Reasoning`；Chat 适配器解析流里的 `delta.reasoning_content`，并在后续请求里把历史 assistant 消息的 `reasoning_content` 原样回传（没有思维链时不发该字段）。DeepSeek 官方文档要求带 `tools` 的请求回传它，否则可能返回 400；实测当前服务端尚未强制，但随时可能变严。
- 新增 `reasoning.delta` 事件（仅用于展示，不落盘）；`message.completed` 与会话 `assistant` 记录带完整 `reasoning`。`message.completed` 另带 `ReasoningTokensEst`：内核对思维链的 token 估算，界面在服务端没报告思维链 token 时显示它；它由 `reasoning` 算出，不落盘，会话恢复时重新计算。估算规则只在内核里有一份（`agent.EstimateTokens`），界面不自己估算。
- 思维链 token：`agent.Usage` 与 `usage` 事件增加 `ReasoningTokens`（属于 `OutputTokens` 的一部分，provider 不报告时为 0）。Chat 取 `usage.completion_tokens_details.reasoning_tokens`（OpenAI 与 DeepSeek 文档里的字段），Responses 取 `usage.output_tokens_details.reasoning_tokens`。会话 `usage` 记录相应增加 `reasoning_tokens`，为 0 时省略；旧会话没有这个字段，读出来是 0。`usage` 事件紧跟在同一次模型调用的 `message.completed` 之后发出，TUI 只把紧挨着的那个 `usage` 算到这次调用的思考上。真实 DeepSeek 是否返回这个字段尚未验证。
- 缓存 token：`agent.Usage` 与 `usage` 事件增加 `CacheReadTokens`（命中缓存）和 `CacheWriteTokens`（写入缓存），都是 `InputTokens` 的一部分，provider 不报告时为 0。**`InputTokens` 统一为“全部输入”的口径**，各协议在适配器里归一：Chat 取 `usage.prompt_tokens_details.cached_tokens`（OpenAI）或 `usage.prompt_cache_hit_tokens`（DeepSeek），两个都有时取大的；Responses 取 `usage.input_tokens_details.cached_tokens`；Gemini 取 `usageMetadata.cachedContentTokenCount`，这三家的输入本来就含缓存；Anthropic 的 `input_tokens` 不含缓存，适配器把 `cache_read_input_tokens` 与 `cache_creation_input_tokens` 加回去（`message_delta` 里的累计值与 `message_start` 取大的，不重复相加）。只有 Anthropic 报告缓存写入，其余协议的 `CacheWriteTokens` 恒为 0。会话 `usage` 记录相应增加 `cache_read_tokens`、`cache_write_tokens`，为 0 时省略，旧会话读出来是 0。**这些字段都是按各家文档实现的，只有 `httptest` 验证，没有对真实服务确认过。**
- Responses 适配器不处理思维链。

**重试、并行、上下文管理、会话恢复（后补）**

- 事件新增 `retrying`（第几次、最多几次、等多久、原因）和 `compacted`（`mode` 为 `prune` / `summary`，估算的前后 token、被丢弃的条数、完整摘要、失败原因、`manual` 是否由用户主动触发）。`tool.finished` 新增 `Content`：回给模型的完整文本。
- **重试**：`ProviderError.Retryable` 的错误按指数退避重试，默认 3 次（约 1s、2s、4s，±25% 抖动，单次上限 16s），`Retry-After` 优先但超过 30 秒就不等；`insufficient_quota` 不重试。**本次调用一旦向界面输出过内容就不再重试**，否则已显示（CLI 里已写进 stdout）的半截答案会被重复输出。`Config.Retry` 为 nil 用默认策略，`&RetryPolicy{}` 关闭。
- **并行工具**：同一批 tool call 并发执行，上限 `Config.MaxParallelTools`（默认 4，1 为串行）。worker 只执行工具，事件发送和历史写入都在 agent 主 goroutine，所以 `event.Sink` 不要求线程安全。`tool.requested` 按调用顺序、`tool.finished` 按完成顺序，历史里的 tool 消息始终按调用顺序。`Tool.Run` 必须并发安全。工具 panic 只算该次调用失败，不再作废整轮。
- **上下文管理**：`Config.ContextWindow` > 0 时，每次调用模型前估算输入 token，超过预算（窗口减去至多 25% 的输出预留，再取 80%）就压缩：先从最旧的开始把较长的工具输出裁成“开头 200 字符 + 标记”（幂等），按 token 估算降到预算的 85% 以下就停，不一次裁光；裁完从服务端校准值里扣掉被裁的部分，不复位成纯估算（纯估算整体偏高，会让下一步又判定超限）。不够再把更早的对话总结成摘要。摘要分层：每次压缩只把新切掉的那一段总结成新的一层，追加在已有各层之后，不重新消化旧摘要；超过 3 层时把最旧的两层合并成一层，所以最近的几段保持详细，越旧的被压得越狠，而不是每次整份重写、一层层失真。合并失败不算压缩失败（新的一层已经有了，摘要暂时多一层，下次再合并）；合并时被取消则整体放弃，历史和摘要保持原样。摘要对外仍是一个字符串，各层之间用一行固定的分隔文字隔开（同时告诉模型后面的内容更晚），没有分隔文字的旧会话摘要按单层处理。摘要附在系统提示词后，历史只保留切点之后的内容；切点只选在用户消息处，不拆 assistant 的 tool_calls 与它的结果，本轮的用户消息和最近一轮总是保留。服务端报告超长时压缩一次再重试，不占步数、每步只做一次。摘要失败（出错、被截断、为空）历史保持原样、本轮继续、同一轮不再重试。摘要请求的输出上限不低于 4000：思考模式的推理 token 也计入上限，沿用用户较小的 `maxOutput` 会让摘要被截断（真实 DeepSeek 上发现）。
- **会话记录**：`tool` 记录新增 `content`；新增 `compact`（`mode`、`tokens_before/after`、`dropped`、`summary`、`error`、`manual`）、`retry`、`resume` 记录。`manual` 为 true 表示用户主动触发，缺省为 false（旧会话没有这个字段，按自动压缩读）。
- **手动压缩（`/compact`）**：`Agent.Compact(ctx, focus, sink)` 在两轮之间把**全部**历史总结成摘要，历史清空；不像自动压缩那样保留最近一轮原文，因为用户要的就是把前面的收起来，而摘要提示词本身要求写明未完成的事项和下一步。它和自动压缩共用同一套分层摘要（`foldSummary`），所以也会追加成新的一层、超过 3 层合并最旧两层。`focus` 是用户对摘要的额外要求（`/compact 保留所有文件路径`），去掉首尾空白、最多 500 字符，追加在摘要提示词末尾；为空时提示词与自动压缩逐字节相同。它与 `Run` 共用同一把锁，会等当前一轮结束再执行。历史为空返回 `ErrNothingToCompact`，不调用模型也不发事件；被取消返回 `ctx.Err()`，历史和摘要原样、不发事件；摘要失败发一个带 `Err` 的 `compacted` 事件（`manual` 为 true）并返回该错误，同样不动历史。事件同时发给调用方的 sink 和 `Config.Recorder`，所以会话落盘后恢复得到同样的历史，且重放出的事件仍带 `manual`，界面不会把它当成自动压缩来描述。手动压缩不是一轮：不写 `user` / `turn` 记录，不计入会话轮数。
  - TUI：压缩期间不能发送消息，也不能 `/model`、`/new`、`/resume`、再次 `/compact`；活动行显示「压缩上下文」、耗时和「Ctrl+C 取消」，Ctrl+C 只取消压缩、不退出程序；退出时 `Shutdown` 会取消并等它收尾。TUI 经 `Options.Compactor` 注入，在 `tea.Cmd` 里调用，`cmd/sprout` 的适配器每次取当前的 agent（`/model` 会替换它）。`sprout run` 是单次运行，不提供手动压缩。
  - 对话区的内容不变，只追加一条提示；之后发给模型的请求只带摘要和新消息。
- **会话恢复**：`session.Load` 重放出历史、摘要和给 TUI 的事件流，规则与运行时一致——并行工具结果按调用顺序而非完成顺序；进程被杀留下的未完成 tool call 补“未执行”；首次模型调用失败或被取消时撤销用户消息；panic 整轮作废；压缩按当初方式重做（裁剪按记录里的条数从最旧的开始裁同样多条，复用纯函数 `agent.PruneToolOutputs`；旧记录是一次裁光，条数就是候选总数，重放结果不变）。损坏的行跳过并计数。`Recorder.Attach` 让恢复后的对话接在原会话文件后面。选择器：`latest`（`-c`）、列表序号（≤3 位数字）、ID 前缀；列表按文件修改时间排序，因为恢复后继续追加不会改文件名。模型或服务商与原会话不同时丢弃旧思维链。`sprout run -c` 找不到会话是错误（退出码 1）；TUI 里则提示并开新会话。恢复期间 TUI 禁止发送。
- 关键测试是“黄金对照”：用真实 agent 跑含并行工具、思维链、裁剪、摘要的长对话，重放出的历史与摘要必须与运行时逐条一致，并带前提断言和变异检查。

**尚未验证**

- OpenAI 官方服务（Chat 与 Responses）：只验证过 DeepSeek。
- 真实终端里的 TUI 手工操作，尤其是中文输入法下 Enter 的组合期行为、Windows 控制台里 Ctrl+C 的送达方式、鼠标滚轮是否被送达、拖选复制在各终端里的表现（OSC 52 是否生效、Linux 缺 xclip/xsel/wl-clipboard 与 Termux 缺 termux-clipboard-set 时的提示、反色高亮的观感）、Ctrl+Home/End 等组合键在各终端里是否被正确识别、流式渲染的观感。TUI 只用 `tea.Program` 加桩 runner 和模型级渲染测试验证过。
- 在非终端环境（stdin 重定向到 `/dev/null`）下启动 TUI 不会自行退出，会一直挂到被外部终止；正常使用不受影响，M1 未处理。
- 工具子进程会继承 `sprout` 的环境变量，包括通过 `DEEPSEEK_API_KEY` 等传入的 API Key；模型可以让命令读取它。M1 没有权限模块，这是已知取舍。
- `go test -race`（本机没有 gcc）。
