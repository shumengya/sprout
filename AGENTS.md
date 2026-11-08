# AGENTS.md

给在本仓库工作的编码 agent。只写代码里读不出来、又会影响正确性的信息；通用规范不在这里重复。

## 项目是什么

Sprout 是 Go 编写的跨平台通用型 agent runtime：内核是 ReAct 循环，TUI / WebUI / CLI / JSON-RPC 四种入口共用同一套会话与工具。模块路径 `github.com/shumengya/sprout`，Go 1.27+。

当前处于 **M0（设计与文档）**，没有可运行代码。M1 范围：ReAct 循环、OpenAI / Anthropic provider、`bash` + `powershell` 两个工具、CLI、JSONL 会话。未经用户确认，不要提前实现后续里程碑。

## 常用命令

M1 初始化 `go.mod` 后，提交前依次执行：

```bash
go build ./...
go test ./...
go vet ./...
smygolint ./...
```

M0 没有 Go 代码，不要为了“让命令能跑”先建空包或空测试。

## 架构边界

- `internal/agent` 是内核，不得 import 任何 `internal/surface/*`；surface 只能通过 `internal/event` 的事件与命令和内核交互。
- provider、tool、session 存储都以接口注入；内核不 import 具体厂商 SDK。
- 平台差异（shell、路径、终端）只放在 `internal/platform`，其他包不判断 `GOOS`。
- 单二进制、无 CGO；不引入 SQLite 驱动、系统库绑定等依赖，保证 Termux 可编译。
- 不建万能 `utils` 包；共享类型放 `internal/event` 或对应领域包。

## 已定技术决策

| 主题 | 决策 |
| --- | --- |
| LLM 协议 | OpenAI Chat Completions → OpenAI Responses → Anthropic Messages，按此顺序实现；其他供应商通过 `models.jsonc` 配置，不写死在代码里 |
| 会话 | JSONL 追加写，按 agent 工作目录分组：`<数据目录>/sessions/<工作目录 slug>/<session-id>.jsonl`，同目录放 `meta.json` |
| 工具 | M1 只有 `bash` 和 `powershell`；文件、搜索、MCP、skills 等由用户后续决定 |
| 权限 | 暂不实现权限模块，默认完全权限；不要提前加审批流或 allow/deny 配置 |
| TUI | Bubble Tea v2 + lipgloss + bubbles + glamour；不用 tview |
| 平台 | Windows 优先开发，目标 Linux / macOS / Android Termux |

数据目录默认 `~/.sprout`，后续用配置项或环境变量覆盖。

## 项目特有的坑

- `models.jsonc` 带注释，`encoding/json` 直接解析会失败；加载前必须去注释，或改用支持 JSONC 的解析器。
- OpenAI Responses 与 Chat Completions 的请求、响应、流式事件结构都不同；不要用一个字符串拼接的“兼容层”把两者混在一起。
- Windows 上 `bash` 工具不能假设 Git Bash 存在；shell 选择统一走 `internal/platform`。`powershell` 工具默认调用 `powershell.exe`（兼容 5.1），不要默认 `pwsh`。
- Termux 没有 systemd、没有 CGO 工具链；不要引入依赖这些的库。
- 所有路径处理用 `filepath`，不要手拼 `/` 或 `\`。

## 完成标准

- 行为变更补测试；测试用 fake provider / fake tool，不打真实 API。
- 提交前必须通过 build / test / vet / smygolint；smygolint 报错必须清零，不要靠缩小检查范围绕过。
- 不删除、不跳过失败测试；确需跳过时说明原因。
- 涉及协议、事件、会话格式的改动，同步更新 `docs/` 下的设计文档（M1 起建立）。

## 不要做

- 不要越过里程碑提前实现第三个工具、权限模块、MCP、WebUI 或子代理。
- 不要复制 Crush / Codex / Claude Code 的代码；参考设计可以，注意 Crush 是 FSL-1.1-MIT。
- 不要提交 `.env`、API key、`auth.json`、构建产物。
- 不要为了“修复”依赖冲突去改 `go.sum`；先说明冲突。

## 深入文档

- 产品定位、四种入口、路线图：`README.md`
- Go 编码规范：加载 `golang-spec` skill，不要在本文件复述规则
- 设计文档（M1 起放在 `docs/`）：架构、协议、决策记录。
