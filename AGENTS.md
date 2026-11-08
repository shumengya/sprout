# AGENTS.md

本文件面向在本仓库工作的编码 agent。开始任务前先读完；与用户当次指令冲突时，以用户指令为准。

## 项目概览

Sprout 是一个用 Go 编写的跨平台通用型 agent runtime。内核是 ReAct（Reason + Act）循环，TUI / WebUI / CLI / JSON-RPC 是四种并列入口，共用同一套内核、工具与会话。

- 模块路径：`github.com/shumengya/sprout`（首次发布前可调整）
- Go 版本：1.27+
- 当前阶段：M0 设计，只有文档，没有可运行代码

## 已定决策

1. 通用型 agent，不绑定编码场景；编码能力是工具集与提示词的一种组合。
2. 自研，参考其他 agent 的公开设计，不 fork、不复制代码。
3. 四种入口并列：TUI（主入口）、WebUI、CLI、JSON-RPC API。
4. 完全跨平台；开发期 Windows 优先，后续 Linux / macOS / Android Termux。
5. 单二进制、无 CGO，便于 Termux 等环境部署。
6. 文档与注释用中文，标识符用英文。

## 架构不变量

违反任一条时先停下来讨论，不要直接改：

1. **内核与界面解耦**：`internal/agent` 不得 import 任何 `internal/surface/*`。
2. **单一交互协议**：surface 只通过类型化事件流（内核 → 界面）和命令（界面 → 内核）交互，不直接调用 provider 或 tool。
3. **依赖倒置**：内核依赖 `Provider`、`Tool`、`Store` 等接口，不依赖具体实现或厂商 SDK。
4. **平台差异收敛**：`GOOS` 判断只允许出现在 `internal/platform`；其他包用接口或构建标签。
5. **可测试**：内核不直接读写终端、不直接 `os.Exit`、不依赖全局状态；时间、随机数、IO 尽量注入。
6. **无 CGO**：不引入需要 CGO 的依赖（SQLite 驱动、系统库绑定等），保证 Termux 可编译。

## 目录规划

尚未创建，按里程碑逐步落地：

```text
cmd/sprout/            主入口与子命令
internal/agent/        ReAct 循环、消息编排、停止条件、预算
internal/provider/     LLM 抽象与各厂商实现
internal/tool/         工具接口、注册表、内置工具
internal/session/      会话模型与持久化
internal/event/        类型化事件与命令定义
internal/permission/   工具授权与审批策略
internal/config/       配置加载与校验
internal/platform/     shell、路径、终端等平台差异
internal/surface/tui/  终端界面
internal/surface/web/  WebUI 模块
internal/surface/cli/  非交互命令行
internal/surface/rpc/  JSON-RPC 服务
docs/                  设计文档与 ADR
```

依赖方向：`surface → agent → provider / tool / session`。反向依赖通过接口注入。跨层共享类型放 `internal/event` 或对应领域包，不建万能 `utils`。

## 开发约定

### Go 规范（强制）

写任何 Go 代码前加载 `golang-spec` skill，按《树萌芽 Go 规范》逐条执行；收工前运行：

```bash
smygolint ./...
```

退出码非 0 必须改到通过。不要为了过检查去改标准库、模块缓存或 vendor。

### 通用约定

- 错误一律用 `fmt.Errorf("动作: %w", err)` 包装，判断用 `errors.Is` / `errors.As`。
- 所有可能阻塞的调用接收 `context.Context`，并在边界处检查取消。
- 并发用 `errgroup` 或 `sync.WaitGroup.Go`；共享状态显式加锁或走 channel，不用全局变量。
- 日志用 `log/slog`，输出结构化字段；库代码不直接打印到 stdout。
- 配置优先级：命令行 > 环境变量 > 配置文件 > 默认值。
- 不在 `init()` 里做 IO 或注册全局状态。

### 命令

```bash
go build ./...
go test ./...
go vet ./...
smygolint ./...
```

M0 阶段尚无 `go.mod`，以上命令在模块初始化后生效。

### 提交

- 一个提交只做一件事；提交前跑 build、test、smygolint。
- 提交信息格式：`<type>(<scope>): <中文 subject>`，例如 `feat(agent): 实现 ReAct 循环骨架`。
- 不用 `git add -A` / `git add .`，只暂存本次改动。
- 不提交密钥、构建产物、临时文件。

## 里程碑与边界

- **M0**：文档与核心接口草图。当前阶段，不写业务代码。
- **M1**：ReAct 循环、OpenAI 兼容 provider、基础工具、CLI、JSONL 会话。
- **M2**：TUI。
- **M3**：JSON-RPC。
- **M4**：WebUI。
- **M5**：MCP、skills、子代理、更多 provider。

未经用户确认，不跨里程碑提前实现。

## 未决问题

实现前必须先与用户确认，不要自行拍板：

- TUI 技术栈（Bubble Tea v2 / tview / 自绘）
- provider 接入顺序与原生协议范围
- 会话存储格式（JSONL / SQLite / 其他）
- 配置格式（TOML / YAML / JSON）
- 权限模型与审批交互
- 扩展机制（MCP、skills、Go 插件）
- 模块路径与 License

## 术语

- **ReAct**：Reason + Act 循环，不是 React 前端框架。
- **Surface**：面向用户或外部程序的入口层。
- **Turn**：一次输入到 agent 给出最终回复的完整过程。
- **Tool call**：模型请求调用工具的一次结构化输出。
