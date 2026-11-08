# Step 1：TUI 骨架 + 配置 + 假 Agent

> 范围：只做可运行的 TUI 外壳和事件链路，不接网络、不检测 shell、不写会话文件。
> 目的：尽早暴露 Bubble Tea v2、Windows 终端、布局、取消、配置解析这些平台问题。

## 1. 这一步要得到什么

双击 `bin/sprout.exe` 后：

1. 进入全屏 TUI，显示模型、cwd、配置状态。
2. 输入一句话回车，假 agent 以流式方式回显。
3. 输入包含“工具”或 `tool` 的内容，显示一条假的工具调用卡片。
4. `Ctrl+C` 能取消正在回显的回合；空闲时退出。
5. `Ctrl+D` 退出；窗口缩放不花屏；中文和 emoji 正常显示。
6. `~/.sprout/models.jsonc` 不存在时自动生成带注释的模板，并提示填入 API Key。
7. 配置有语法错误时，显示文件名、行、列，而不是 panic。

这一步**不验证**模型、工具执行、会话持久化。

## 2. 技术栈与依赖

| 依赖 | 用途 |
| --- | --- |
| `charm.land/bubbletea/v2` | TUI 主循环 |
| `charm.land/lipgloss/v2` | 样式与布局 |
| `charm.land/bubbles/v2` | `viewport`、`textarea`、`spinner` |
| `github.com/tailscale/hujson` | JSONC 解析 |
| 标准库 | 其他全部 |

注意：Charm v2 的模块路径是 `charm.land/...`，不是 `github.com/charmbracelet/...`。版本先按 Crush 使用的组合锁定：`bubbletea/v2 v2.1.0`、`lipgloss/v2 v2.0.6`、`bubbles/v2 v2.2.1`。

Bubble Tea v2 与 v1 的差异：

- `View()` 返回 `tea.View`，通过 `v.AltScreen = true` 开启全屏。
- 键盘消息是 `tea.KeyPressMsg`，不是 `tea.KeyMsg`。
- 程序用 `tea.NewProgram(m, tea.WithContext(ctx))` 启动。

## 3. 文件清单

```text
go.mod / go.sum
.gitignore                            忽略 bin/
cmd/sprout/main.go                    参数、配置加载、TUI 启动、错误暂停
internal/agent/event/event.go         事件类型与 Sink
internal/agent/runner.go              Runner 接口
internal/agent/fake/fake.go           假 agent：流式回显 + 假工具卡片
internal/agent/config/config.go       JSONC 解析、校验、默认模型解析
internal/agent/config/template.go     内置 models.jsonc 模板
internal/agent/config/config_test.go
internal/agent/fake/fake_test.go
internal/agent/platform/console_windows.go
internal/agent/platform/console_other.go
internal/tui/model.go                 主模型与状态
internal/tui/update.go                消息处理
internal/tui/view.go                  布局与渲染
internal/tui/entries.go               transcript 条目
internal/tui/styles.go                lipgloss 样式
internal/tui/model_test.go
```

## 4. 事件与 Runner

### 事件

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

type Tool struct {
    Name      string
    Command   string
    Output    string
    ExitCode  int
    Truncated bool
    Duration  time.Duration
}

type Event struct {
    Type      Type
    At        time.Time
    Text      string
    Tool      *Tool
    Error     string
    Cancelled bool
}

type Sink func(Event)
```

Step 1 只要求 TUI 能消费这些事件；真实 agent 在 Step 2/3 替换 fake runner，事件结构不变。

### Runner

```go
type Runner interface {
    Run(ctx context.Context, prompt string, emit event.Sink) error
}
```

约定：

- `Run` 可以在独立 goroutine 中执行。
- 必须至少发出 `turn.started` 和 `turn.finished`；出错时先发 `error`。
- 取消时返回 `ctx.Err()`，并发出 `turn.finished{Cancelled:true}`。
- TUI 侧仍会兜底：如果 goroutine 返回但没收到 `turn.finished`，补一个错误条目并恢复空闲。

### Fake Runner

- 把回显文本按 rune 切块，每 20ms 发一个 `message.delta`，避免拆坏中文。
- 文本包含“工具”或 `tool` 时，先发一对假的 `tool.requested` / `tool.finished`，再发最终文本。
- 延迟可配置，测试里设为 0。

## 5. 配置加载

### 路径

- 默认：`~/.sprout/models.jsonc`（`os.UserHomeDir()` + `filepath.Join`）。
- 覆盖：`--config <path>` 或 `SPROUT_CONFIG`。
- 首次运行：目录不存在则 `MkdirAll(0700)`；文件不存在则写入内置模板（`0600`），并返回一条 warning，不直接退出。

### 解析

1. `hujson.Standardize` 去注释、去尾逗号。
2. `json.Unmarshal` 到结构体。
3. 语法错误：`hujson` 的解析错误已经带行列，直接展示；类型错误只有字节偏移，再换算行列。错误类型带 `Path`、`Line`、`Column`、`Message`。
4. 校验：
   - `default` 必须是 `provider/model`，且两边都存在。
   - `protocol` 只允许 `openai-chat`、`openai-responses`。
   - `baseUrl` 必须是 http/https，去掉结尾 `/`。
   - `apiKeyEnv` 优先；为空时用 `apiKey`；两者都空只产生 warning，Step 1 不阻止启动（fake agent 不需要 key）。

### TUI 展示

- 正常：`model: deepseek/deepseek-chat`、`cwd: ...`、`config: ...`。
- 首次生成或缺少 key：黄色提示行，说明文件路径和下一步操作。
- 解析失败：启动前打印错误到控制台，等待回车退出；不进入 TUI。

## 6. TUI 设计

### 主模型

```go
type Model struct {
    cfg      *config.Config
    runner   agent.Runner
    width    int
    height   int
    ready    bool

    entries  []entry
    viewport viewport.Model
    input    textarea.Model
    spinner  spinner.Model

    running  bool
    follow   bool
    cancel   context.CancelFunc
    events   chan event.Event
}
```

### 布局

- Header：2 行（标题 + 模型/cwd/配置状态）。
- Transcript：`viewport`，占据剩余高度。
- Status：1 行，显示 spinner / 当前状态 / 快捷键提示。
- Input：`textarea`，默认 1 行，最高 6 行，带边框。

每次 `tea.WindowSizeMsg` 或输入框高度变化时重算：

```text
viewportHeight = height - headerHeight - statusHeight - inputHeight
```

所有高度 `max(1, ...)`，避免小窗口 panic。

### 事件流

```text
输入回车
  → 追加 user 条目
  → 创建 ctx/cancel + 事件 channel
  → goroutine 调 runner.Run
  → runner emit → channel → waitForEvent cmd → tea.Msg → Update
  → message.delta 追加到当前 assistant 条目
  → tool.* 生成或更新工具条目
  → turn.finished 恢复空闲
```

`waitForEvent` 每次只读一个事件，处理后重新挂起；channel 缓冲 64。取消时调用 `cancel()`，继续消费直到 `turn.finished`，避免 goroutine 卡住。

### 按键

| 按键 | 行为 |
| --- | --- |
| `Enter` | 发送；输入框为空或正在运行时忽略 |
| `Ctrl+J` | 换行（先于 Enter 判断） |
| `Ctrl+C` | 运行中取消；空闲退出 |
| `Ctrl+D` | 退出 |
| `PgUp` / `PgDn` | 滚动 transcript，并暂停/恢复自动跟随 |

注意：`textarea` 默认把 Enter 当换行，必须在 `Update` 中先拦截 Enter，再转发给 textarea。

### 条目渲染

- `user`：`你> ` + 文本。
- `assistant`：纯文本，流式追加；`turn.finished` 后标记完成。
- `tool`：`⚙ <name> <command>`，下一行 `└ N 行，exit X`，最多显示 8 行输出。
- `error`：红色，显示在 transcript 中。

Step 1 不做 Markdown、不做鼠标、不做代码高亮。

## 7. Windows 控制台

- `main` 启动前调用 `platform.InitConsole()`。
- Windows 实现：`GetConsoleOutputCP` / `SetConsoleOutputCP(65001)`、`GetConsoleCP` / `SetConsoleCP(65001)`，返回 restore 函数，退出时恢复。
- 非 Windows：no-op。
- 双击启动且配置加载失败时，打印错误后 `按 Enter 退出`，避免窗口闪退。
- 不加 `-H windowsgui`，保证双击有控制台。

## 8. 实现顺序

1. `go mod init github.com/shumengya/sprout`，加依赖，`.gitignore` 加 `/bin/`。
2. `event` + `Runner` + `fake`，配单元测试。
3. `config` 解析、模板、校验、行列错误、单元测试。
4. TUI 主模型：header、viewport、textarea、status、窗口尺寸。
5. 提交/流式事件/取消/假工具卡片。
6. `main` 装配、Windows 控制台、双击错误暂停。
7. 手工验收 + `go test ./...` + `go vet ./...`。

## 9. 验收清单

- [ ] `go build -o bin/sprout.exe ./cmd/sprout` 成功。
- [ ] 双击 `bin/sprout.exe` 进入全屏 TUI，显示模型、cwd、配置路径。
- [ ] 输入“你好”后逐字流式回显，不是一次性出现。
- [ ] 输入“用一下工具”显示假工具卡片和截断输出。
- [ ] 生成中 `Ctrl+C` 取消并恢复输入；空闲 `Ctrl+C`/`Ctrl+D` 退出。
- [ ] 窗口缩放到很小不 panic；恢复尺寸后布局正常。
- [ ] 终端里输入中文、emoji、长行不破坏布局。
- [ ] 首次运行生成 `~/.sprout/models.jsonc`；已有文件不覆盖。
- [ ] 配置加注释、尾逗号能解析；删掉一个逗号能报出行列。
- [ ] `go test ./...`、`go vet ./...` 通过。
- [ ] Step 1 全程不发起任何网络请求。

## 10. 已知问题与风险

1. **Bubble Tea v2 API 与网上示例不一致**：多数教程是 v1。按 Crush 的源码写，并锁版本；不要混用 `github.com/charmbracelet/...` 和 `charm.land/...`。
2. **Enter 与中文输入法**：输入法组合期间的 Enter 不应发送。先手工测试；如果出现误发送，再按 Bubble Tea v2 的 key/paste 消息类型做过滤，不要在 textarea 外层用启发式判断。
3. **双击时的 cwd**：Explorer 启动的工作目录不一定是 exe 所在目录。需要实测；如果落在 `System32`，回退到 exe 目录，并记录 warning。
4. **legacy conhost 的 UTF-8**：`SetConsoleOutputCP(65001)` 能解决大部分情况，但旧字体可能缺字；Windows Terminal 优先。README 里写清楚。
5. **goroutine/channel 泄漏**：取消后 runner 必须退出，TUI 必须继续读到 `turn.finished`；测试里用 `-race` 跑取消路径。
6. **小窗口布局**：header + input 可能超过总高度。所有高度计算要 clamp，`View` 里对超长行做截断，避免 viewport 负高度。
7. **JSONC 行列定位**：`hujson` 语法错误自带行列；`json.Unmarshal` 类型错误只有字节偏移，中文配置需要按 rune 换算列号。两步分别写测试。
8. **TUI 里不能打日志到 stdout**：Bubble Tea 接管输出。Step 1 不引入日志；后续用 `~/.sprout/logs/` 文件。

## 11. 需要确认的取舍

- Step 1 不写会话 JSONL，放到 Step 4。
- Step 1 不做 shell 检测，header 只显示配置状态。
- Step 1 只做 TUI，不做 `sprout run`；单次 CLI 放到 Step 4。
- 假 agent 用“工具”/`tool` 关键词触发假工具卡片，仅用于验证 UI。
- 首次运行自动生成 `models.jsonc`，不覆盖已有文件。
