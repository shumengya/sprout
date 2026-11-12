# AGENTS.md

给在本仓库工作的编码 agent。只写代码里读不出来、又会影响正确性的信息；通用规范不在这里重复。

## 项目是什么

Sprout 是 Go 编写的跨平台通用型 agent runtime：内核是 ReAct 循环，TUI / WebUI / CLI / JSON-RPC 四种入口共用同一套会话与工具。模块路径 `github.com/shumengya/sprout`，Go 1.27+。

当前处于 **M1（最小可用闭环）**，ReAct 循环、OpenAI Chat Completions + Responses、`bash` + `powershell`、Bubble Tea v2 TUI、`sprout run`、JSONL 会话已实现。M1 范围与实现差异见 `docs/m1-minimal.md`、`docs/m1-implementation.md`（§15 列出了与原计划的差异和尚未验证的部分）。未经用户确认，不要提前实现后续里程碑。

## 常用命令

提交前依次执行：

```bash
go build ./...
go test ./...
go vet ./...
```

开发时用 `go run ./cmd/sprout --data-dir ./data/.sprout`；`go run` 的二进制在临时目录，不显式指定数据目录会报错。构建发布二进制：`go build -o bin/sprout.exe ./cmd/sprout`（`bin/` 与 `data/` 已在 `.gitignore`）。

测试里的 shell 用例使用本机真实的 Git Bash / pwsh / PowerShell 5.1，没有对应 shell 时自动跳过。`-race` 需要 CGO 工具链，本项目本身无 CGO，按需在有 gcc 的环境里跑。

## 架构边界

- `internal/agent` 是内核，不得 import `internal/tui`、`internal/cli`、`internal/webui`、`internal/rpc`；surface 只能通过 `internal/agent/event` 的事件与命令和内核交互。
- provider、tool、session 存储都以接口注入；内核不 import 具体厂商 SDK。
- 平台差异（shell、路径、终端）只放在 `internal/agent/platform`，其他包不判断 `GOOS`。
- 单二进制、无 CGO；不引入 SQLite 驱动、系统库绑定等依赖，保证 Termux 可编译。
- 不建万能 `utils` 包；共享类型放 `internal/agent/event` 或对应领域包。目录职责见 README「项目结构」。

## 已定技术决策

| 主题 | 决策 |
| --- | --- |
| LLM 协议 | OpenAI Chat Completions → OpenAI Responses → Anthropic Messages，按此顺序实现；其他供应商通过 `models.jsonc` 配置，不写死在代码里 |
| 配置格式 | 用户会编辑的文件一律 JSONC（`models.jsonc` 等）；`meta.json`、会话 JSONL、缓存用普通 JSON/JSONL |
| 会话 | JSONL 追加写，按 agent 工作目录分组：`<数据目录>/sessions/<工作目录 slug>/<session-id>.jsonl`，同目录放 `meta.json` |
| 数据目录 | 默认 `<exeDir>/data/.sprout`（安装级/系统级，跟随二进制）；`--data-dir` / `SPROUT_DATA_DIR` 覆盖；默认不写 `~/.sprout` |
| 工具 | 内置 `bash`、`powershell`、`read`、`write`；必须在数据目录的 `tools.jsonc` 里 `"enable": true` 才注册给模型（见 `docs/tools.md`）；搜索、MCP、skills 等由用户后续决定 |
| 权限 | 暂不实现权限模块，默认完全权限；不要提前加审批流或 allow/deny 配置 |
| TUI | Bubble Tea v2 + lipgloss + bubbles；不用 tview |
| 平台 | Windows 优先开发，目标 Linux / macOS / Android Termux |

数据目录默认 `<exeDir>/data/.sprout`，不写 `~/.sprout`。后续如需用户级/项目级覆盖，再在安装级之上叠加。

## 项目特有的坑

- `models.jsonc` 带注释，`encoding/json` 直接解析会失败；加载前必须去注释，或改用支持 JSONC 的解析器。
- OpenAI Responses 与 Chat Completions 的请求、响应、流式事件结构都不同；不要用一个字符串拼接的“兼容层”把两者混在一起。
- Windows 的 `bash` 工具只认真实 Git Bash / MSYS2 / Cygwin：按 `bashPath` 配置、Program Files 两个路径、PATH 顺序查找；只有 `System32\bash.exe`（WSL 启动器）时视为不可用，并提示安装 Git for Windows。
- Windows 的 `powershell` 工具先找 `pwsh.exe`（7+），再回退 `powershell.exe`（5.1）；把 edition 写进工具描述，5.1 不要生成 `&&` / `||` / `?:`。
- `bash` 和 `powershell` 都是一等公民，不排先后；是否注册由 `tools.jsonc` 开关、平台和实际检测结果共同决定，模型只看到当前可用工具。
- 新增内置工具时同步改三处：`config.KnownTools`（否则 `tools.jsonc` 校验会拒绝该名字）、`config.ToolsTemplate` 与仓库根的 `tools.example.jsonc`（测试保证一致）、`cmd/sprout/setup.go` 的 `registerTools`。
- Termux 没有 systemd、没有 CGO 工具链；不要引入依赖这些的库。
- 数据目录用 `os.Executable()` 定位，`go run` 时二进制在临时目录；开发时显式传 `--data-dir ./data/.sprout`，不要用 cwd 当默认。目录不可写时明确报错，不要静默回退到 `~/.sprout`。
- 所有路径处理用 `filepath`，不要手拼 `/` 或 `\`。

## 完成标准

- 行为变更补测试；单元测试用 `httptest` 或接口替身，不打真实 API。测试替身只能出现在 `_test.go`，不能进入默认运行路径。
- 提交前必须通过 build / test / vet；不要靠注释掉测试或缩小检查范围绕过。
- 不删除、不跳过失败测试；确需跳过时说明原因。
- 涉及协议、事件、会话格式的改动，同步更新 `docs/` 下的设计文档（M1 起建立）。
- 每次完成任务（验证通过并提交）后，自动执行一次 `build.cmd`（非 Windows 用 `CGO_ENABLED=0 go build -o bin/sprout ./cmd/sprout`）更新 `bin/sprout.exe`，不用让用户自己构建；构建失败时报告原因。Windows 上 `sprout.exe` 正在运行时文件被占用会构建失败，提示用户先退出再重试。

## 不要做

- 不要越过里程碑提前实现 `bash` / `powershell` / `read` / `write` 之外的工具、权限模块、MCP、WebUI 或子代理。
- 不要为 TUI 或演示引入假模型、假工具、回显模式；测试替身只允许放在 `_test.go`。
- 不要复制 Crush / Codex / Claude Code 的代码；参考设计可以，注意 Crush 是 FSL-1.1-MIT。
- 不要提交 `.env`、API key、`auth.json`、构建产物。
- 不要为了“修复”依赖冲突去改 `go.sum`；先说明冲突。

## 深入文档

- 产品定位、四种入口、路线图：`README.md`
- M1 最小实现计划：`docs/m1-minimal.md`
- M1 详细实现：`docs/m1-implementation.md`
- 设计文档（M1 起放在 `docs/`）：架构、协议、决策记录。
