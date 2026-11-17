# AGENTS.md

给在本仓库工作的编码 agent。只写代码里读不出来、又会影响正确性的信息；通用规范不在这里重复。

## 项目是什么

Sprout 是 Go 编写的跨平台通用型 agent runtime：内核是 ReAct 循环，TUI / WebUI / CLI / JSON-RPC 四种入口共用同一套会话与工具。模块路径 `github.com/shumengya/sprout`，Go 1.27+。

M1（最小可用闭环）、M2（识图）已实现，M5 中的 MCP、Agent Skills 与子代理也已实现：ReAct 循环、OpenAI Chat Completions + Responses、Anthropic Messages、Gemini、`bash` + `powershell` + `read` + `write` + `edit` + `delete` 与外部命令工具、Bubble Tea v2 TUI（含 `/mcp`、`/skills` 面板）、`sprout run`、`sprout update`、JSONL 会话与恢复、自动重试与上下文压缩、MCP 客户端（`mcp.jsonc`；工具、资源、提示词、elicitation、sampling、MRTR 与 OAuth 登录 `sprout mcp login`）、技能注册表（`skills.jsonc`）与手动触发（`/技能名 任务`）、子代理（`agent` 工具，`agents/*.md` 自定义类型）；M3 JSON-RPC 也已实现：`sprout serve` 提供对话、取消、会话管理与模型、思考程度切换（`docs/rpc.md`）。M1 范围与实现差异见 `docs/m1-minimal.md`、`docs/m1-implementation.md`（§15 列出了与原计划的差异和尚未验证的部分），识图设计见 `docs/m2-vision.md`，MCP、技能与子代理见 `docs/mcp.md`、`docs/skills.md`、`docs/subagents.md`，逐项的完成与待办状态见 `TODOS.md`。未经用户确认，不要提前实现后续里程碑（WebUI）。

## 常用命令

提交前依次执行：

```bash
go build ./...
go test ./...
go vet ./...
```

开发时用 `go run ./cmd/sprout --data-dir ./data/.sprout`；`go run` 的二进制在临时目录，不显式指定数据目录会报错。构建发布二进制：`go build -o bin/sprout.exe ./cmd/sprout`（`bin/` 与 `data/` 已在 `.gitignore`）。

测试里的 shell 用例使用本机真实的 Git Bash / pwsh / PowerShell 5.1，没有对应 shell 时自动跳过。`-race` 需要 CGO 工具链，本项目本身无 CGO，按需在有 gcc 的环境里跑。

发布：`release.cmd <版本号>`（非 Windows 用 `go run ./tools/release`），版本号是日期（如 `2026.11.12`，可加 `-rc.1` 后缀）。它会跑完 build / vet / test，再交叉编译八个目标并打包到 `release/<版本号>/`（已在 `.gitignore`）。Termux 目标必须用 `GOOS=android`，不能复用 `linux_arm64` 的产物（Android 的 seccomp 禁止 `faccessat2`，linux 版会 SIGSYS 崩在启动，见 `docs/发布.md`）。发布说明正文写在 `docs/releases/<版本号>.md`，随仓库提交；下载表与校验说明由工具生成。流程细节见 `docs/发布.md`。

## 架构边界

- `internal/agent` 是内核，不得 import `internal/tui`、`internal/cli`、`internal/webui`、`internal/rpc`；surface 只能通过 `internal/agent/event` 的事件与命令和内核交互。
- provider、tool、session 存储都以接口注入；内核不 import 具体厂商 SDK。
- 平台差异（shell、路径、终端）只放在 `internal/agent/platform`，其他包不判断 `GOOS`。
- 单二进制、无 CGO；不引入 SQLite 驱动、系统库绑定等依赖，保证 Termux 可编译。这是硬约束，不可让步。
- 其他依赖按成本判断，不设禁令。看的是「纯 Go 可交叉编译」和「`go.mod` 实际增加多少间接依赖」，不是依赖数量本身：
  为一个小功能引入一个大模块（只用到其中一小部分）通常不划算；自己实现超过约两周工作量、或涉及密码学细节容易写错的（签名验证、密钥处理），优先引入成熟库；上游标注 experimental 的部分不比自写更可靠。
- 不建万能 `utils` 包；共享类型放 `internal/agent/event` 或对应领域包。目录职责见 README「项目结构」。

## 已定技术决策

| 主题 | 决策 |
| --- | --- |
| LLM 协议 | OpenAI Chat Completions → OpenAI Responses → Anthropic Messages → Google Gemini，四种均已实现；其他供应商通过 `models.jsonc` 配置，不写死在代码里 |
| 模型参数 | `contextWindow`、`maxOutput` 优先级：`models.jsonc` 手写 > models.dev 本地缓存 > 默认值（400000 / 16000）；价格只来自 models.dev，没有默认值。缓存由 `sprout models update` 更新，启动不联网 |
| 会话 | JSONL 追加写，按 agent 工作目录分组：`<数据目录>/sessions/--<工作目录 slug>--/<yyyymmdd-hhmmss>_<id>.jsonl`（slug 规则见 `session.Slug`），同目录放 `meta.json` |
| 数据目录 | 默认 `<exeDir>/data/.sprout`（安装级/系统级，跟随二进制）；`--data-dir` / `SPROUT_DATA_DIR` 覆盖；默认不写 `~/.sprout` |
| 工具 | 内置 `bash`、`powershell`、`read`、`write`、`edit`、`delete`，另可在 `tools.jsonc` 里用 `exec` 把外部命令包装成工具；条目必须 `"enable": true` 才注册给模型（见 `docs/tools.md`） |
| MCP | 自带最小客户端（`internal/agent/mcp`，stdio + Streamable HTTP），配置在数据目录 `mcp.jsonc`；工具注册为 `mcp__<服务端>__<工具>`；启动时**后台**连接（界面不等服务端），连好后重建 agent 接入工具；协商版本缓存在 `<数据目录>/mcp-versions.json`；OAuth 令牌存在 `<数据目录>/mcp-auth.json`，只能由 `sprout mcp login` 在终端登录；elicitation / sampling 只在界面接了处理函数时才在握手里声明；当前自写客户端，是否引入外部库按「架构边界」里的依赖成本规则判断（见 `docs/mcp.md`） |
| Skills | 注册表 `<数据目录>/skills.jsonc`：只有注册的技能才生效，每条含 `path` 与 `enable`；扫描 `<工作目录>/.sprout/skills`、`<工作目录>/.agents/skills`、`<数据目录>/skills`、`~/.agents/skills` 发现的技能自动登记，已有条目不覆盖；`scanAgents` 为 false 时不加载 `.agents` 下的技能。只把元数据追加进系统提示词，正文由模型用 `read` 按需读取；`/技能名 任务` 手动触发时由 TUI 读出正文交给模型（见 `docs/skills.md`） |
| 子代理 | 内置工具 `agent`：同步、只有一层（子代理的工具表里永远没有 `agent`），同一批调用并行；类型 = 内置 general / explore + `<数据目录>/agents/*.md` + `<工作目录>/.sprout/agents/*.md`（后者优先）；子代理过程记在 `<会话名>.agents/<call id>.jsonl`，主会话只记 `subagent_usage`（见 `docs/subagents.md`） |
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
- 系统提示词改动只改 `agent.DefaultPromptTemplate`，并同步仓库根的 `SYSTEMPROMPT.example.md`（测试保证一致）；模板里用到的字段名（`promptData`）是用户可见接口，不要改名。
- 新增内置工具时同步改三处：`config.KnownTools`（否则 `tools.jsonc` 校验会拒绝该名字）、`config.ToolsTemplate` 与仓库根的 `tools.example.jsonc`（测试保证一致）、`cmd/sprout/setup.go` 的 `registerTools`。
- 外部工具（`internal/agent/tool/external`）只做「把参数替换进命令再执行」，不校验参数取值；覆盖 `parameters` 只影响模型看到的 schema，不是执行期约束。别把它当成沙箱。
- Termux 没有 systemd、没有 CGO 工具链；不要引入依赖这些的库。
- MCP 客户端目前是自己写的，但这不是禁令：要不要换成官方 SDK 或引入某个窄库，按「架构边界」里的依赖成本规则判断。注意 MCP **客户端**做 OAuth 只需要 PKCE、授权服务器元数据发现和令牌存储，不需要 JWT / JWKS 校验——校验令牌签名是资源服务端的职责，别把 JWT 当成客户端的引入成本。改协议细节时同时更新 `docs/mcp.md`。
- MCP 连接不能在启动路径上同步等待：`buildApp` 只调 `startMCP` 起后台 goroutine，工具就绪后由 `applyMCP` 重建 agent。重建必须发生在没有 `Run` 进行时（TUI 空闲时或 run 模式第一次提问前），否则进行中那一轮的消息会丢；TUI 的 `mcpReadyMsg` 只是置标记，实际接入在 `Update` 末尾的空闲检查里。
- OAuth 令牌只存 `mcp-auth.json`（0600、临时文件改名写入），不进日志、`mcp-versions.json` 或错误信息。登录的安全校验（S256、issuer / resource 一致、回调 state 与 Host、RFC 9207 `iss`、端点必须 https 或回环 http）都有测试，别为了兼容某家服务商放宽；回调里 state 不对的请求只回 400、不结束登录。
- `mcp-versions.json` 只存目标指纹的 SHA-256，不存 URL 原文：URL / args 里可能有 `${VAR}` 展开出的令牌。缓存只能用来跳过 `server/discover` 试探，任何握手失败都必须能退回完整协商。
- 技能的目录段是追加在系统提示词末尾的（`skills.AppendCatalog`），不要改成模板变量：用户的 `SYSTEMPROMPT.md` 可能是旧版本，没有那个变量的模板会让技能静默失效。
- `mcp.jsonc` 的模板（`config.MCPTemplate`）与仓库根的 `mcp.example.jsonc` 由测试保证一致，改一处要同步另一处。
- 状态栏的项目名、内置行号和内置颜色都在 `internal/tui/statusline.go` 的 `statusDefs` 里，`statusItemOrder` 必须与它一一对应（测试保证）。加一项时这三处一起改，另外 `config.StatuslineTemplate` 与仓库根的 `statusline.example.jsonc` 也要同步；`statusline.jsonc` 的项目名是用户可见接口，不要改名。
- `mcp.jsonc` 与 `skills.jsonc` 的开关由面板写回，必须走 `config` 包的 `editJSONC`（按字节偏移拼接，保留注释、BOM、缩进，写完重新校验，失败回滚）；不要读出结构体再整份 `json.Marshal` 写回，会丢掉用户的注释。唯一的例外是旧版 `{"disabled": [...]}` 的一次性迁移（`WriteSkills`）。
- 技能的注册表键名优先于 `SKILL.md` 里的 `name`；`/技能名` 触发时内置命令优先，名字不是技能（`ErrUnknownSkill`）就当普通提问，所以 `/etc/hosts` 这类输入仍能原样发送。
- 子代理在主代理的工具 goroutine 里装配（`cmd/sprout/subagent.go` 的 `spawnSubagent`），此时 TUI / RPC 线程可能同时在改 app：`sel` / `key` 必须在 `a.mu` 下读，思考档位从 `a.ag.Thinking()` 取，不要读 `a.effort`（`/effort` 一轮中途也能改它）。子代理的进度只能经 `tool.Call.Emit` 交给内核转发，不能直接写主代理的 sink：Sink 约定只在 agent 主 goroutine 里调用。
- `sprout update` 依赖 tag 与产物文件名同 `tools/release` 生成的一致（`sprout_<版本>_<平台>.<后缀>`）；改发布命名或平台名时要同步 `cmd/sprout/update.go` 的 `platformName`。它不需要数据目录。自动替换必须先通过 `SHA256SUMS.txt` 校验和新程序的 `--version` 试运行，任何一步失败都不能动原程序；Windows 正在运行的 exe 不能覆盖，只能先改名成 `.old`（下次启动时清理），所以别改成直接写文件。
- 数据目录用 `os.Executable()` 定位，`go run` 时二进制在临时目录；开发时显式传 `--data-dir ./data/.sprout`，不要用 cwd 当默认。目录不可写时明确报错，不要静默回退到 `~/.sprout`。
- 所有路径处理用 `filepath`，不要手拼 `/` 或 `\`。

## 完成标准

- 行为变更补测试；单元测试用 `httptest` 或接口替身，不打真实 API。测试替身只能出现在 `_test.go`，不能进入默认运行路径。
- 提交前必须通过 build / test / vet；不要靠注释掉测试或缩小检查范围绕过。
- 不删除、不跳过失败测试；确需跳过时说明原因。
- 涉及协议、事件、会话格式的改动，同步更新 `docs/` 下的设计文档（事件见 `docs/events.md`，会话格式见 `docs/session-format.md`）。
- 每次完成任务（验证通过并提交）后，自动执行一次 `build.cmd`（非 Windows 用 `CGO_ENABLED=0 go build -o bin/sprout ./cmd/sprout`）更新 `bin/sprout.exe`，不用让用户自己构建；构建失败时报告原因。Windows 上 `sprout.exe` 正在运行时文件被占用会构建失败，提示用户先退出再重试。

## 不要做

- 不要越过里程碑提前实现权限模块或 WebUI。子代理已按用户要求实现同步、一层的 `agent` 工具；后台子代理、给运行中的子代理追加消息等扩展要先经用户确认。MCP 的工具结果图片、Resources、Prompts、Elicitation、MRTR、Sampling、OAuth 均已按用户要求实现（剩余限制见 `docs/mcp.md`「已知限制」），在此之外的 MCP 能力（如 `resources/subscribe`、url 模式 elicitation、CIMD）仍要先经用户确认。`sprout update` 只更新程序本身，不要顺带改数据目录里的配置。
- 不要为 TUI 或演示引入假模型、假工具、回显模式；测试替身只允许放在 `_test.go`。
- 不要复制 Crush / Codex / Claude Code 的代码；参考设计可以，注意 Crush 是 FSL-1.1-MIT。
- 不要提交 `.env`、API key、`auth.json`、构建产物。
- 不要为了“修复”依赖冲突去改 `go.sum`；先说明冲突。

## 深入文档

- 产品定位、四种入口、路线图：`README.md`
- 已实现 / 未实现 / 待验证的逐项清单：`TODOS.md`
- M1 最小实现计划：`docs/m1-minimal.md`
- M1 详细实现与落地后的差异：`docs/m1-implementation.md`
- 识图（图片输入）：`docs/m2-vision.md`
- 工具注册与外部命令工具：`docs/tools.md`
- MCP 客户端与 `/mcp` 面板：`docs/mcp.md`
- 技能注册表、`/skills` 面板与手动触发：`docs/skills.md`
- 子代理（`agent` 工具、自定义类型、记录与事件）：`docs/subagents.md`
- 系统提示词与 `AGENTS.md` 读取：`docs/system-prompt.md`
- 思考程度（effort）：`docs/effort.md`
- 命令行、斜杠命令、快捷键参考：`docs/CLI命令.md`、`docs/斜杠命令.md`、`docs/快捷键.md`
- 发布流程：`docs/发布.md`
- 事件协议（surface 与内核的接口，顺序保证与字段语义）：`docs/events.md`
- JSON-RPC（`sprout serve` 的方法、通知、错误码）：`docs/rpc.md`
- 会话格式（JSONL 记录、`meta.json`、恢复重放规则）：`docs/session-format.md`
- 已定决策及其原因：`docs/decisions.md`
