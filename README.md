# Sprout（萌芽）

跨平台、通用型、可嵌入的 Go Agent Runtime。内核是一个 ReAct（Reason + Act）循环，界面只是它的外壳。

## 是什么

现有 agent 大多把「内核」和「界面」焊死在一起：终端版不能给网页用，SDK 版又缺交互能力。Sprout 把两者拆开：

- **内核**：ReAct 循环、LLM 调用、工具执行、会话状态、权限控制。
- **外壳**：TUI、WebUI、CLI、JSON-RPC 四种入口，共用同一套内核、工具和会话。

同一个会话可以在终端里开始，在网页里继续，也可以被其他程序通过 JSON-RPC 调起。内核不假设自己在跟人还是跟程序打交道。

## 设计目标

- **通用**：不绑定编码场景。编码只是内置工具集与提示词的一种组合。
- **模块化**：core / provider / tool / surface 之间只通过接口和事件通信。
- **跨平台**：Windows 优先开发，目标覆盖 macOS、Linux、Android Termux；单二进制，不依赖 CGO。
- **可控**：工具调用有明确的权限边界，行为可审计、可复现。
- **自研**：参考现有 agent 的公开设计，不 fork、不复制代码。

## 四种入口

| 入口 | 场景 | 形态 |
| --- | --- | --- |
| TUI | 日常交互（主入口） | 全屏终端界面，流式输出与工具调用可视化 |
| WebUI | 远程 / 图形化操作 | 独立模块，浏览器访问同一内核 |
| CLI | 脚本组合 | 非交互，stdin/stdout，适合管道与自动化 |
| API | 被其他程序调用 | 默认 JSON-RPC，可作为 SDK 后端 |

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
                    │ Session / Permission /  │
                    │ Config / Platform       │
                    └─────────────────────────┘
```

关键约束：

1. 内核不 import 任何 surface 包。
2. surface 只通过「事件流 + 命令」与内核交互，不直接调用工具或 provider。
3. provider、tool、会话存储都可替换。
4. 平台差异（shell、路径、终端）收敛在 `internal/platform`。
5. WebUI 作为独立模块，优先通过 RPC 接入内核，避免与内核进程耦合。

## 交互协议（初版）

内核与所有 surface 共用一套类型化协议，定义在 `internal/event`：

- **Command**（界面 → 内核）：`prompt`、`cancel`、`approve`、`resume`。
- **Event**（内核 → 界面）：`turn.started`、`message.delta`、`tool.requested`、`tool.finished`、`usage`、`turn.finished`、`error`。

TUI、WebUI、CLI、JSON-RPC 都只是这套协议的适配器。

## 当前状态

**设计阶段（M0）。** 仓库目前只有文档，没有可运行代码。

## 路线图

- **M0 设计**：README、AGENTS.md、核心接口草图。
- **M1 内核 + CLI**：ReAct 循环、OpenAI 兼容 provider、基础工具、JSONL 会话。（provider 与存储均为暂定默认）
- **M2 TUI**：主入口，流式输出与工具审批。
- **M3 JSON-RPC**：会话管理、流式事件、取消。
- **M4 WebUI**：独立前端模块，复用 JSON-RPC 事件。
- **M5 扩展**：MCP、skills、子代理、更多 provider。

## 快速开始

尚未实现。M1 完成后的目标用法：

```bash
go build ./cmd/sprout

sprout                 # TUI
sprout run "..."       # 单次非交互
sprout serve           # JSON-RPC
sprout web             # WebUI
```

## 非目标

- 不做 Claude Code / Codex 的协议兼容层。
- v1 不追求多 agent 编排。
- 不为跨平台牺牲内核的可测试性。
- 不引入需要 CGO 的依赖。

## 参考

设计上参考了 Crush、Codex、Claude Code、pi、opencode 等项目的公开实现与文档，只借鉴模式，不复制代码。

## License

TBD
