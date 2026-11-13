# Sprout（萌芽）

跨平台、通用型、可嵌入的 Go Agent Runtime。内核是一个 ReAct（Reason + Act）循环，TUI / WebUI / CLI / JSON-RPC 只是它的外壳。

## 是什么

现有 agent 大多把「内核」和「界面」焊死在一起：终端版不能给网页用，SDK 版又缺交互能力。Sprout 把两者拆开：

- **内核**：ReAct 循环、LLM 调用、工具执行、会话状态。
- **外壳**：TUI、WebUI、CLI、JSON-RPC 四种入口，共用同一套内核、工具和会话。

同一个会话可以在终端里开始，在网页里继续，也可以被其他程序通过 JSON-RPC 调起。内核不假设自己在跟人还是跟程序打交道。

## 设计目标

- **通用**：不绑定编码场景。编码只是内置工具集与提示词的一种组合。
- **模块化**：core / provider / tool / surface 之间只通过接口和事件通信。
- **跨平台**：Windows 优先开发，目标覆盖 macOS、Linux、Android Termux；单二进制，不依赖 CGO。
- **可审计**：完整事件流落盘，会话可重放；权限模块后续作为独立模块加入。
- **自研**：参考现有 agent 的公开设计，不 fork、不复制代码。

## 四种入口

| 入口 | 场景 | 形态 |
| --- | --- | --- |
| TUI | 日常交互（主入口） | 全屏终端界面，流式输出与工具调用可视化 |
| WebUI | 远程 / 图形化操作 | 独立模块，浏览器访问同一内核 |
| CLI | 脚本组合 | 非交互，stdin/stdout，适合管道与自动化 |
| API | 被其他程序调用 | 默认 JSON-RPC，可作为 SDK 后端 |

## 技术选型

| 主题 | 选择 | 理由 |
| --- | --- | --- |
| TUI | Bubble Tea v2 + lipgloss + bubbles（Markdown 用 goldmark 解析、自己画成终端文本，代码高亮用 chroma，不依赖 glamour） | v2 于 2026-02 稳定；Crush 同栈；纯 Go，Termux 可编译 |
| LLM 协议 | OpenAI Chat Completions → OpenAI Responses → Anthropic Messages → Google Gemini | 先覆盖最广的兼容协议，再补新协议与原生协议 |
| 模型配置 | `models.jsonc`（JSONC，可写注释） | 供应商、模型、能力、默认参数集中配置；用户配置统一 JSONC |
| 内部状态 | JSON / JSONL | 用户很少直接编辑，保持严格、好解析 |
| 会话 | JSONL 追加写，按工作目录分组 | 无 CGO、可恢复、可重放；目录形如 `<exeDir>/data/.sprout/sessions/<工作目录>/<session-id>.jsonl` |
| 数据目录 | `<exeDir>/data/.sprout`（安装级/系统级） | 跟随二进制，不写用户 home 或项目目录；`--data-dir` / `SPROUT_DATA_DIR` 可覆盖 |
| 工具 | `bash`、`powershell`、`read`、`write` | 由数据目录的 `tools.jsonc` 显式启用（`"enable": true`）才会交给模型，见 `docs/tools.md` |
| 系统提示词 | 数据目录的 `SYSTEMPROMPT.md`（Go `text/template` 模板） | 缺失或为空时写入内置模板；环境、工具、识图等动态内容用 `{{.Cwd}}`、`{{if .Has "bash"}}` 等占位，见 `docs/system-prompt.md` |
| 用户说明 | 数据目录与工作目录各级的 `AGENTS.md` | 全局说明 + 从上到下的项目说明，追加在系统提示词末尾，见 `docs/system-prompt.md` |
| 权限 | 暂不实现 | 默认完全权限；后续做成独立模块，不混进内核 |

## 项目结构

```text
cmd/sprout/              主入口：解析参数、装配组件、启动入口
internal/
  agent/                 ReAct 内核
    event/               事件与命令协议
    session/             JSONL 会话
    config/              models.jsonc 与运行配置
    platform/            shell / 路径 / 终端差异
    tool/                工具接口与注册表
      shell/             bash / powershell 共用的进程执行器
      bash/
      powershell/
      file/              read / write 文件工具
      external/          tools.jsonc 里用 exec 声明的外部命令工具
    skills/              技能发现与加载（占位，未实现）
    mcp/                 MCP 客户端与工具接入（占位，未实现）
  provider/              模型渠道商
    openai/              Chat Completions + Responses
    anthropic/           Messages
    google/              Gemini
    transport/           四种协议共用的 HTTP / SSE 传输层
  media/                 图片读取、类型嗅探与大小限制
  tui/                   Bubble Tea v2 终端界面
  cli/                   非交互 CLI
  webui/                 WebUI 模块（占位，未实现）
  rpc/                   JSON-RPC 服务（占位，未实现）
docs/                    设计文档
```

小模块（事件、会话、配置、平台、工具、skills、MCP）挂在内核 `agent/` 下；`provider`、`media`、`tui`、`cli`、`webui`、`rpc` 是平级的大组件。所有组件在同一个 Go module 内，靠包边界隔离，暂不拆成多个 module。对外 API 稳定前统一放在 `internal/`，需要对外嵌出时再加 `pkg/sprout` 门面。

## 架构

```text
┌─────────────┬─────────────┬─────────────┬─────────────┐
│     TUI     │    WebUI    │     CLI     │  JSON-RPC   │
└──────┬──────┴──────┬──────┴──────┬──────┴──────┬──────┘
       │             │             │             │
       └─────────────┴──────┬──────┴─────────────┘
                            │  Event ↑   Command ↓
                    ┌───────┴────────┐
                    │  Agent Runtime │  ReAct 循环
                    └───┬────────┬───┘
                        │        │
              ┌─────────┴──┐  ┌──┴─────────┐
              │  Provider  │  │    Tool    │
              │ (LLM API)  │  │  Registry  │
              └────────────┘  └──┬─────────┘
                                 │
                    ┌────────────┴────────────┐
                    │ Session / Config /      │
                    │ Platform                │
                    └─────────────────────────┘
```

关键约束：

1. 内核不 import 任何 surface 包。
2. surface 只通过「事件流 + 命令」与内核交互，不直接调用工具或 provider。
3. provider、tool、会话存储都可替换。
4. 平台差异（shell、路径、终端）收敛在 `internal/agent/platform`。
5. WebUI 作为独立模块，优先通过 RPC 接入内核，避免与内核进程耦合。

## 交互协议（初版）

内核与所有 surface 共用一套类型化协议，定义在 `internal/agent/event`：

- **Command**（界面 → 内核）：`prompt`、`cancel`、`resume`。
- **Event**（内核 → 界面）：`turn.started`、`message.delta`、`reasoning.delta`、`message.completed`、`tool.requested`、`tool.finished`、`usage`、`retrying`、`compacted`、`turn.finished`、`error`。

TUI、WebUI、CLI、JSON-RPC 都只是这套协议的适配器。

## 当前状态

**M1（最小可用闭环）与 M2（识图）已实现。** 终端界面、单次运行、OpenAI Chat / Responses、Anthropic Messages、Gemini 四种协议、`bash` / `powershell` / `read` / `write` 与外部命令工具、图片输入、JSONL 会话与恢复、自动重试、上下文压缩均可用；JSON-RPC、WebUI、MCP、skills 等后续里程碑尚未开始。已实现与未实现的逐项清单见 `TODOS.md`；真实模型服务和真实终端的手工验证状态见 `docs/m1-implementation.md` §12、§15 与 `TODOS.md` 第二节。

## 路线图

- **M0 设计**：README、AGENTS.md、M1 详细计划（`docs/m1-minimal.md`）。
- **M1 内核 + TUI + CLI**（已完成）：ReAct 循环、OpenAI Chat Completions + Responses、`bash` / `powershell`、Bubble Tea v2 TUI、单次 CLI、JSONL 会话。
- **M2 识图**（已完成）：四种协议的图片输入、会话保存图片、不支持视觉的模型自动降级，见 `docs/m2-vision.md`。
- **M3 JSON-RPC**：会话管理、流式事件、取消。
- **M4 WebUI**：独立前端模块，复用 JSON-RPC 事件。
- **M5 扩展**：MCP、skills、子代理、搜索与 web 等更多工具。

M1 与 M2 之间还陆续补入了 Anthropic Messages、Gemini、`read` / `write`、外部命令工具、会话恢复与上下文压缩，它们不单独占里程碑编号。

## 快速开始

```bash
go build -o bin/sprout.exe ./cmd/sprout

sprout                 # 终端界面
sprout run "..."       # 单次非交互，答案写 stdout，进度写 stderr
sprout -c              # 恢复当前目录最近的会话（也可 sprout run -c "..."；TUI 里用 /resume 选择）
sprout --effort high   # 指定思考程度（TUI 里用 /effort 切换），档位由 models.jsonc 按模型声明
sprout models update   # 从 models.dev 更新模型窗口、输出上限和价格的本地缓存（启动时不联网）
```

在 TUI 里输入 `/install-sprout` 可以把当前程序注册成全局 `sprout` 命令（Windows 改用户 PATH，Linux / macOS 在 `~/.local/bin` 建符号链接），`/uninstall-sprout` 卸载。注册是原地调用，配置和会话仍在程序所在目录。

首次运行会在 `<程序所在目录>/data/.sprout/models.jsonc` 生成带注释的配置，填入 API Key（或设置对应环境变量，如 `DEEPSEEK_API_KEY`）后重启即可。可用 `--data-dir` 或 `SPROUT_DATA_DIR` 指定数据目录；用 `go run` 开发时必须显式指定，例如 `go run ./cmd/sprout --data-dir ./data/.sprout`。

后续里程碑的目标用法（尚未实现）：

```bash
sprout serve           # JSON-RPC
sprout web             # WebUI
```

## 非目标

- 不做 Claude Code / Codex 的协议兼容层。
- v1 不追求多 agent 编排。
- M1 不做权限审批与沙箱。
- 不引入需要 CGO 的依赖。

## 参考

设计上参考了 Crush、Codex、Claude Code、pi、opencode 等项目的公开实现与文档，只借鉴模式，不复制代码。

TUI 选型依据：[Charm v2 公告](https://charm.land/blog/v2/)、[GitLab CLI 从 tview 迁移到 Bubble Tea](https://gitlab.com/groups/gitlab-org/-/epics/19748)、[Console 对 Bubble Tea v2 的评测](https://console.dev/tools/bubble-tea-v2)。

## 发布与下载

版本号用日期，如 `2026.11.12`。每个版本提供 Windows（x64 / ARM64）、Linux（x64 / ARM64 / ARMv7）、macOS（Intel / Apple 芯片）、Android Termux（ARM64）八个平台的压缩包，单二进制、无 CGO。Termux 包与 Linux ARM64 包内容相同，单独取名只是方便直接找到。

发布说明见 `docs/releases/<版本号>.md`；打包流程、校验方法与发布前检查见 `docs/发布.md`。

## License

[Apache License 2.0](LICENSE)。使用时请保留 `LICENSE` 与版权声明；本项目不对适用性作任何担保。

Copyright 2026 shumengya
