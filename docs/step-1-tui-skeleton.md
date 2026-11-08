# Step 1：TUI 骨架 + 配置 + 真实 shell 工具

> 范围：全屏 TUI、安装级数据目录、JSONC 配置、真实的 `bash` / `powershell` 工具。
> 不接 LLM、不写会话、不做 `sprout run`；聊天部分先用假 agent 回显。

## 1. 这一步要得到什么

双击 `bin/sprout.exe` 后：

1. 进入全屏 TUI，显示模型、cwd、数据目录、已检测到的工具。
2. 输入一句话回车，假 agent 流式回显，验证 TUI 事件链路。
3. `/tools` 列出当前可用工具、路径和 PowerShell 版本。
4. `/tool powershell Get-Location` 执行真实 PowerShell 命令，显示输出、退出码、耗时。
5. 有 Git Bash 时 `/tool bash pwd` 执行真实 Bash 命令；没有时明确提示不可用及原因。
6. `Ctrl+C` 取消正在执行的工具或回显；空闲时退出；`Ctrl+D` 退出。
7. 首次运行在 `<exe目录>/data/.sprout/models.jsonc` 生成带注释的模板。
8. 配置有语法错误时显示文件、行、列，不 panic。

这一步**不验证**模型调用、ReAct 循环、会话持久化。

## 2. 技术栈与依赖

| 依赖 | 用途 |
| --- | --- |
| `charm.land/bubbletea/v2` | TUI 主循环 |
| `charm.land/lipgloss/v2` | 样式与布局 |
| `charm.land/bubbles/v2` | `viewport`、`textarea`、`spinner` |
| `github.com/tailscale/hujson` | JSONC 解析 |
| 标准库 | HTTP 之外的进程执行、配置、文件 |

Charm v2 的模块路径是 `charm.land/...`。版本按 Crush 的组合锁定：`bubbletea/v2 v2.1.0`、`lipgloss/v2 v2.0.6`、`bubbles/v2 v2.2.1`。

Bubble Tea v2 关键差异：

- `View()` 返回 `tea.View`，用 `v.AltScreen = true` 开启全屏。
- 键盘消息是 `tea.KeyPressMsg`。
- 启动：`tea.NewProgram(m, tea.WithContext(ctx))`。

## 3. 数据目录

默认数据根目录：

```text
<二进制所在目录>/data/.sprout/
├── models.jsonc
├── sessions/          （Step 4 使用）
└── logs/              （后续使用）
```

- 这是**安装级 / 系统级**数据目录，跟随二进制，不跟随用户 home，也不跟随项目。
- 解析顺序：`--data-dir` > `SPROUT_DATA_DIR` > `<exeDir>/data/.sprout`。
- 用 `os.Executable()` 取二进制路径，`filepath.Dir` 取目录。
- 目录不存在时 `MkdirAll(0700)`；不可写时明确报错，并提示用 `--data-dir` 覆盖。
- `go run` 时二进制在临时目录，开发时显式传 `--data-dir ./data/.sprout`。
- 后续如需用户级/项目级覆盖，再在安装级之上叠加，不改变默认值。

## 4. 文件清单

```text
go.mod / go.sum
.gitignore                            忽略 bin/
cmd/sprout/main.go                    参数、数据目录、配置、TUI 启动
internal/agent/event/event.go         事件类型与 Sink
internal/agent/runner.go              Runner 接口（本步只有 fake）
internal/agent/fake/fake.go           假 agent：流式回显
internal/agent/config/config.go       JSONC 解析、校验、默认模型
internal/agent/config/paths.go        数据目录解析
internal/agent/config/template.go     内置 models.jsonc 模板
internal/agent/config/config_test.go
internal/agent/tool/tool.go           Tool 接口、Result、Registry
internal/agent/tool/shell/shell.go    通用进程执行、超时、头尾截断
internal/agent/tool/bash/bash.go      Bash 工具
internal/agent/tool/powershell/powershell.go
internal/agent/tool/shell/shell_test.go
internal/agent/platform/shell_windows.go
internal/agent/platform/shell_unix.go
internal/agent/platform/console_windows.go
internal/agent/platform/console_other.go
internal/tui/model.go                 主模型与状态
internal/tui/update.go                消息、斜杠命令、事件处理
internal/tui/view.go                  布局与渲染
internal/tui/entries.go               transcript 条目
internal/tui/styles.go                lipgloss 样式
internal/tui/model_test.go
```

## 5. 工具系统

### 接口

```go
type Result struct {
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

- `Run` 返回 `error` 只表示基础设施失败（如进程无法启动）；命令退出码非 0 属于正常结果。
- 参数统一是 `{"command":"..."}`，为 Step 3 的 LLM 工具调用做准备。
- Registry 提供 `Register`、`Get`、`List`，TUI 和后续 agent 共用。

### 检测

**Windows PowerShell**：

1. 配置 `shell.powershellPath`
2. `pwsh.exe` on PATH
3. `%ProgramFiles%\PowerShell\7\pwsh.exe`
4. `powershell.exe` on PATH
5. `%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe`

记录 edition：

- `pwsh` → `core`（7+）：支持 `&&` / `||` / 三元 / `??`。
- `powershell` → `desktop`（5.1）：不支持上述语法；默认输出编码不同。

**Windows Bash**：

1. 配置 `shell.bashPath`
2. `%ProgramFiles%\Git\bin\bash.exe`
3. `%ProgramFiles(x86)%\Git\bin\bash.exe`
4. PATH 里的 `bash.exe`（MSYS2 / Cygwin）
5. 只找到 `%SystemRoot%\System32\bash.exe` 时判定不可用，并说明那是 WSL 启动器，需要安装 Git for Windows

**Linux / macOS / Termux**：

- `bash` 从 PATH 查找。
- 不注册 `powershell`（当前决策：PowerShell 工具只在 Windows 提供）。

检测结果包含 `Name`、`Path`、`Kind`、`Edition`、`Available`、`Reason`。`/tools` 原样展示，便于排查。

### 执行

两个工具共用 `shell.Executor`：

- `bash --noprofile --norc -c <command>`。
- `powershell -NoProfile -NonInteractive -Command <command>`，`pwsh` 同理。
- 工作目录固定为启动目录；每次新进程，`cd` 不跨调用保持。
- 继承父进程环境；不接管 stdin；不支持后台任务。
- 默认超时 120 秒，最大 600 秒。
- `stdout` / `stderr` 分开收集；合并展示时标注来源。
- 输出上限 50KB：保留头尾，中间插入 `... [truncated N bytes] ...`。
- 超时通过 `exec.CommandContext` 取消；Windows 上只保证杀掉直接子进程，进程树清理留到后续。
- PowerShell 5.1 先设置控制台 UTF-8；如果中文仍乱码，在命令前注入 `[Console]::OutputEncoding` 并 `exit $LASTEXITCODE`，作为已知风险测试。

### 工具描述

工具描述会原样进入 Step 3 的模型上下文，现在就写清楚：

- `bash`：Windows 上是 Git Bash，路径用 `/d/...`；Linux/macOS 是系统 bash；每次新进程。
- `powershell`：标明 edition；5.1 不要用 `&&`、`||`、`?:`、`??`；不要对原生程序用 `2>&1`。
- 两者都注明：无状态、不要运行交互式命令、不要等待 stdin。

## 6. 配置

### 生成与路径

- 路径：`<dataDir>/models.jsonc`。
- 不存在时写入内置模板（`0600`），返回 warning，不退出。
- 已存在时绝不覆盖。
- `--config` 可单独指定配置文件路径。

### 模板内容

包含 `deepseek`（`openai-chat`）、`openai`（`openai-chat`）、`openai-responses` 三个 provider，以及：

```jsonc
"shell": {
  // 留空则自动检测
  "bashPath": "",
  "powershellPath": ""
}
```

### 解析与校验

1. `hujson.Standardize` 去注释、去尾逗号。
2. `json.Unmarshal` 到结构体；错误带 `Path` / `Line` / `Column` / `Message`。
3. 校验：
   - `default` 必须是 `provider/model` 且存在。
   - `protocol` 只允许 `openai-chat`、`openai-responses`。
   - `baseUrl` 必须是 http/https，去掉结尾 `/`。
   - `apiKeyEnv` 优先，`apiKey` 兜底；都为空只 warning，不阻止 Step 1。
   - `shell.*Path` 若非空，必须是存在的文件。

## 7. 事件与假 Agent

Step 1 没有 LLM，但事件结构按最终形态定义：

```go
type Type string

const (
    TurnStarted   Type = "turn.started"
    MessageDelta  Type = "message.delta"
    ToolRequested Type = "tool.requested"
    ToolFinished  Type = "tool.finished"
    TurnFinished  Type = "turn.finished"
    Error         Type = "error"
)

type Event struct {
    Type      Type
    At        time.Time
    Text      string
    Tool      *ToolResult
    Error     string
    Cancelled bool
}
```

- `Runner` 接口：`Run(ctx, prompt, emit) error`。
- Fake runner 按 rune 切块，每 20ms 发 `message.delta`；延迟可配置，测试设为 0。
- 假 agent 只做文本回显，不伪造工具调用；工具通过 `/tool` 命令真实执行。

## 8. TUI

### 布局

- Header：标题、模型、cwd、数据目录、可用工具数量。
- Transcript：`viewport`，占据剩余高度。
- Status：1 行，spinner / 当前操作 / 快捷键提示。
- Input：`textarea`，默认 1 行，最高 6 行。

高度计算：

```text
viewportHeight = height - headerHeight - statusHeight - inputHeight
```

所有高度 `max(1, ...)`；超长行截断，避免小窗口负高度。

### 斜杠命令

| 命令 | 行为 |
| --- | --- |
| `/tools` | 列出工具名、路径、edition、不可用原因 |
| `/tool <name> <command...>` | 异步执行真实工具，生成 tool 条目 |
| `/help` | 显示可用命令 |
| `/quit` | 退出 |

`/tool` 必须走 `tea.Cmd`，不能在 `Update` 里同步执行。执行期间状态栏显示工具名，`Ctrl+C` 取消。

### 聊天与事件

```text
输入回车
  → 追加 user 条目
  → 创建 ctx/cancel + 事件 channel
  → goroutine 调 runner.Run
  → emit → channel → waitForEvent cmd → Update
  → message.delta 追加到 assistant 条目
  → turn.finished 恢复空闲
```

`/tool` 复用同一套事件：先发 `tool.requested`，执行完发 `tool.finished`，TUI 只负责渲染。

### 按键

| 按键 | 行为 |
| --- | --- |
| `Enter` | 发送/执行命令；运行中忽略 |
| `Ctrl+J` | 输入框换行 |
| `Ctrl+C` | 运行中取消；空闲退出 |
| `Ctrl+D` | 退出 |
| `PgUp` / `PgDn` | 滚动 transcript |

### 条目渲染

- `user`：`你> ` + 文本。
- `assistant`：流式文本。
- `tool`：`⚙ <name> <command>`，下一行 `└ N 行，exit X，耗时 Y`；最多预览 8 行，截断时标注。
- `error`：红色。

不做 Markdown、鼠标、代码高亮。

## 9. Windows 控制台

- `main` 启动 TUI 前调用 `platform.InitConsole()`。
- Windows 设置输入/输出代码页为 65001，退出时恢复。
- 非 Windows no-op。
- 不加 `-H windowsgui`，双击保留控制台。
- 配置/数据目录错误在进入 TUI 前打印，并等待回车退出，避免闪退。

## 10. 实现顺序

1. `go mod init`、依赖、`.gitignore`。
2. `config/paths` + `config` + 模板 + 测试。
3. `tool` 接口/Registry + `shell.Executor` + 截断测试。
4. `platform` shell 检测（Windows / Unix）+ 单元测试。
5. `bash` / `powershell` 工具 + `/tools`、`/tool` 命令。
6. TUI 主模型：header、viewport、textarea、status、窗口尺寸。
7. Fake runner、流式回显、取消。
8. Windows 控制台、main 装配、双击错误暂停。
9. 手工验收 + `go test ./...` + `go vet ./...`。

## 11. 验收清单

- [ ] `go build -o bin/sprout.exe ./cmd/sprout` 成功，双击进入全屏 TUI。
- [ ] 首次运行创建 `bin/data/.sprout/models.jsonc`，内容带注释；已有文件不覆盖。
- [ ] TUI 显示模型、cwd、数据目录、工具数量。
- [ ] `/tools` 正确列出 PowerShell 路径和 edition；有 Git Bash 时列出 bash，没有时显示原因。
- [ ] `/tool powershell Get-Location` 返回真实路径、exit 0、耗时。
- [ ] `/tool bash pwd` 在 Git Bash 环境返回 POSIX 路径；无 Git Bash 时提示不可用。
- [ ] 执行 `exit 1` 的命令，退出码正确显示，TUI 不崩溃。
- [ ] 大输出被头尾截断并标注。
- [ ] 超时命令能取消，状态恢复空闲。
- [ ] 生成中 `Ctrl+C` 取消；空闲 `Ctrl+C`/`Ctrl+D` 退出。
- [ ] 中文、emoji、窗口缩放正常。
- [ ] 配置加注释/尾逗号可解析；删逗号能报行列。
- [ ] `go test ./...`、`go vet ./...` 通过。
- [ ] Step 1 全程不发起网络请求。

## 12. 已知问题与风险

1. **Bubble Tea v2 与网上示例不同**：模块路径 `charm.land/...`，`View() tea.View`，`tea.KeyPressMsg`；按 Crush 源码写并锁版本。
2. **PowerShell 5.1 输出编码**：重定向时可能是 UTF-16LE 或 OEM 代码页；先设控制台 UTF-8，再实测 `中文` 输出。必要时在命令前注入 `[Console]::OutputEncoding`。
3. **Windows 超时只杀直接子进程**：`powershell` 启动的原生程序可能残留；Step 1 先记录限制，后续用 Job Object 解决。
4. **Git Bash 路径**：Windows cwd 交给 bash 后显示为 `/d/...`；向模型描述清楚，不要混用 `C:\` 和 `/d/`。
5. **Enter 与中文输入法**：输入法组合期间的 Enter 可能误发送；必须手工测试，再决定是否过滤 key/paste 消息。
6. **双击 cwd**：Explorer 启动目录可能不是 exe 目录；数据目录用 exe 路径，不依赖 cwd；会话分组后续再处理。
7. **数据目录不可写**：放在 `Program Files` 下会失败；明确报错并提示 `--data-dir`，不静默回退到 `~/.sprout`。
8. **JSONC 行列定位**：`hujson` 语法错误自带行列；`json.Unmarshal` 类型错误只有字节偏移，需要按 rune 换算。
9. **小窗口布局**：所有高度 clamp，超长行截断。
10. **TUI 不能写 stdout 日志**：Step 1 不引入日志；后续写 `<dataDir>/logs/`。

## 13. 取舍

- 数据目录默认安装级：`<exeDir>/data/.sprout`，不写 `~/.sprout`。
- Step 1 实现真实 bash/powershell，但只通过 `/tool` 手动调用；接入 ReAct 在 Step 3。
- 聊天仍是假 agent 回显，不发起网络请求。
- 不写会话、不做 `sprout run`、不做权限审批。
