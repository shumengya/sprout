# 事件协议

内核（`internal/agent`）与各 surface（TUI、CLI、JSON-RPC，以及之后的 WebUI）之间只通过事件交互。定义在 `internal/agent/event`，本包不依赖任何其他内部包。会话落盘也订阅同一条事件流，不另开通道（见 `docs/session-format.md`）。

本文以代码为准；改事件时同步更新本文和 `docs/session-format.md`。

## 1. 入口

- 提交一轮：`Agent.Run(ctx, Input{Text, Images, Queued}, sink)`。`Queued` 可为 nil；非 nil 时内核在每批工具执行完、下一次请求模型之前调用它，取走用户在这一轮进行中排队的输入，依次作为用户消息接在工具结果后面（每条发一个 `user.queued`）。模型给最终答案的那一步之后不再调用，没被取走的由 surface 自己在本轮结束后作为新的一轮发出。它在 agent 主 goroutine 里调用，与界面线程的同步由 surface 负责。
- 手动压缩：`Agent.Compact(ctx, focus, sink)`，与 `Run` 共用一把锁，会等当前一轮结束再执行。
- 取消：取消传入的 `ctx`。事件协议里没有单独的“命令”类型。
- `event.Sink` 只有一个方法 `Emit(Event)`；`event.SinkFunc` 把函数包装成 Sink。事件都由 agent 主 goroutine 发出（并行工具的结果也回到主 goroutine 再发），所以 Sink 不要求线程安全。

## 2. 事件一览

`Kind` 是稳定标识，供序列化和日志使用，JSON-RPC 对外推送时沿用这些名字（线上字段见 `docs/rpc.md`）。

| Kind | Go 类型 | 主要字段 | 落盘为 |
| --- | --- | --- | --- |
| `turn.started` | `TurnStarted` | `Prompt`、`Images` | `user` |
| `user.queued` | `UserQueued` | `Prompt`、`Images` | `user_queued` |
| `message.delta` | `MessageDelta` | `Text` | 不落盘 |
| `reasoning.delta` | `ReasoningDelta` | `Text` | 不落盘 |
| `message.completed` | `MessageCompleted` | `Content`、`Reasoning`、`ToolCalls`、`StopReason`、`ReasoningTokensEst`、`Native` | `assistant` |
| `tool.requested` | `ToolRequested` | `Call{ID, Name, Arguments}` | 不落盘（信息已在 `assistant.tool_calls` 里） |
| `tool.finished` | `ToolFinished` | `ID`、`Name`、`Command`、`Stdout`、`Stderr`、`ExitCode`、`Truncated`、`TimedOut`、`Cancelled`、`Duration`、`Content`、`Err`、`Images` | `tool` |
| `usage` | `Usage` | `InputTokens`、`OutputTokens`、`TotalTokens`、`ReasoningTokens`、`CacheReadTokens`、`CacheWriteTokens` | `usage` |
| `retrying` | `Retrying` | `Attempt`、`Max`、`Delay`、`Reason` | `retry`（仅审计） |
| `compacted` | `Compacted` | `Mode`、`TokensBefore`、`TokensAfter`、`Dropped`、`Summary`、`Err`、`Manual`、`PruneVersion` | `compact` |
| `interrupted` | `Interrupted` | `Reason`、`Content`、`Reasoning`、`Partial` | `interrupted` |
| `error` | `Error` | `Message`、`Retryable` | `error` |
| `turn.finished` | `TurnFinished` | `Reason` | `turn` |
| `subagent` | `Subagent` | `CallID`、`Agent`、`Event`（子代理的一个事件） | 内层是 `Usage` 时为 `subagent_usage`，其余不落盘 |

`TurnFinished.Reason` 取值：`stop`（模型给出最终答案）、`cancelled`（用户取消）、`error`（出错）、`max_steps`（超过 `Config.MaxSteps`）、`length`（输出被长度上限截断）。

## 3. 顺序保证

surface 可以依赖下面这些规则，内核和测试保证它们成立。

- 每轮以一个 `turn.started` 开始，以一个 `turn.finished` 结束，各有且仅有一次。`turn.finished` 是判断“这一轮结束了”的唯一依据。panic 也会被 recover，并以 `turn.finished(error)` 收尾。
- 一轮里可以有多次模型调用（中间夹着工具往返）。每次调用的顺序是：零或多个 `reasoning.delta` / `message.delta`，然后一个 `message.completed`，紧接着一个 `usage`。`usage` 和它前面的 `message.completed` 属于同一次调用，中间不会插别的事件。
- `error` 之后一定跟着 `turn.finished`，`Reason` 为 `error` 或 `max_steps`。
- `interrupted` 之后一定跟着 `turn.finished`，`Reason` 为 `cancelled` 或 `error`。
- 同一批工具调用并行执行（上限 `Config.MaxParallelTools`，默认 4；声明不可并行的工具如 `write` / `edit` / `delete` 独占执行，见 `docs/tools.md`「并行」）：`tool.requested` 按调用顺序发出，`tool.finished` 按完成顺序发出，用 `ID` 配对。被取消而没来得及执行的调用只有 `tool.finished`（`Cancelled` 为 true），没有 `tool.requested`。
- `user.queued` 只出现在一批工具的 `tool.finished` 全部发完之后、下一次模型调用之前，属于当前这一轮，不会另起 `turn.started`。同一批取到几条就连发几条，顺序与排队顺序一致。
- `retrying` 只在本次调用还没向界面输出任何内容时出现，所以重试不会让 surface 收到重复的增量。
- `compacted` 可能出现在一轮中间（自动压缩，`Manual` 为 false），也可能由 `Agent.Compact` 在两轮之间发出（`Manual` 为 true）。手动压缩不是一轮，前后没有 `turn.started` / `turn.finished`。历史被改动就一定有这个事件，压缩从不静默发生。
- `subagent` 只出现在启动它的 `agent` 工具调用的 `tool.requested` 与 `tool.finished` 之间，`CallID` 就是那次调用的 `ID`。内层是子代理自己完整的一轮（`turn.started` … `turn.finished`），同样遵守本节的顺序规则，但不含 `message.delta` / `reasoning.delta`。几个子代理并行时，各自的 `subagent` 事件交错到达，用 `CallID` 区分。

## 4. 字段语义

- **`TurnStarted.Images`**（`UserQueued.Images` 同）：`Data` 是 base64 原始数据。会话记录器落盘后回填 `Path`（相对会话文件的路径），并在自己的副本里清空 `Data`；记录器会先复制切片再改，不影响其他 Sink 收到的事件。`Source` 是图片在消息正文里对应的原始片段，界面用它把路径显示成 `[文件名]`。
- **增量与完整内容**：`message.delta`、`reasoning.delta` 只用于展示。完整的正文和思维链以 `message.completed` 为准；非流式服务只有 `message.completed`，没有增量。
- **`MessageCompleted.ReasoningTokensEst`**：内核对思维链的 token 估算（`agent.EstimateTokens`），供界面在服务端没报告 `Usage.ReasoningTokens` 时显示。它不落盘，恢复会话时重新计算。
- **`MessageCompleted.Native`**：协议原始内容，比如带签名的 Anthropic 思考块、Responses 的 reasoning 条目、Gemini 的 `thoughtSignature`。只有同一协议、同一模型才能回传（`Native.Usable`），界面不需要它。
- **`ToolFinished`**：`Stdout` / `Stderr` 是未经预览截断的完整输出，但已受工具自身 50KB 上限约束。`Content` 是回给模型的完整文本，会话恢复时原样还原。`Err` 非空表示工具没能产出正常结果（参数无效、进程起不来等）。工具的任何失败都只转成 tool 消息回给模型，不终止这一轮。`Images` 是工具正常结束时结果里附带的图片（如 MCP 工具返回的截图），失败或取消时为空；运行时带 `Data`（base64），记录器落盘后回填 `Path`，重放出的事件没有 `Data`。
- **`Usage`**：`InputTokens` 统一是“全部输入”的口径，包含命中缓存（`CacheReadTokens`）和写入缓存（`CacheWriteTokens`）的部分，各协议在适配器里归一。`ReasoningTokens` 是 `OutputTokens` 里用于思维链的部分。provider 不报告的字段为 0。
- **`Retrying`**：`Attempt` 从 1 起。`Delay` 是这次重试前的等待时间；默认策略是 1s、2s、4s… 到 64s，共 7 次，服务端给了 `Retry-After` 时以它为准。
- **`Compacted`**：`Mode` 为 `prune`（裁剪旧工具输出）或 `summary`（把更早的对话总结成摘要）。`TokensBefore` / `TokensAfter` 是估算值。`Dropped` 在 summary 模式下是被摘要替代的消息条数，在 prune 模式下是被裁剪的工具输出条数。`Summary` 是压缩后的完整摘要文本。`Err` 非空表示摘要失败，历史没动，本轮照常继续。`PruneVersion` 只在 prune 模式下有值，是这次裁剪用的策略版本（`agent.PruneVersion`，现在是 2：按工具类型决定保留多少），会话恢复时按它重放；0 表示旧会话，按版本 1（统一保留开头 200 字符）处理。手动压缩带 `KeepLast` 时 `Dropped` 小于历史总条数，最近一轮原文留在历史里。
- **`Interrupted`**：一轮被取消或中途失败、但已经收到部分输出时发出。这一轮不作废：`Content`（已收到的正文加一句中断说明）和 `Reasoning` 作为一条 assistant 消息留在历史里。`Partial` 为 true 表示正文已经流式显示过，界面只需补一行说明，不必把 `Content` 再显示一遍。半截的 tool call 参数可能不是合法 JSON，整个丢弃。
- **`Error.Retryable`**：只是告诉界面这类错误理论上可重试。自动重试已经在内核里做过，到达 surface 的 `error` 是重试耗尽或不可重试的。
- **`Subagent`**：`agent` 工具派出的子代理发出的事件（见 `docs/subagents.md`）。工具在执行中通过 `tool.Call.Emit`（内核放进 ctx 的调用信息）把它交给内核，由主 goroutine 转发，所以 Sink 仍然不要求线程安全。内层的 `Usage` 要计入花费，但它不是主对话的上下文占用；其余内层事件只用于展示进度，不进主代理的历史。

## 5. 给 surface 的约定

- surface 不得 import 内核以外的 surface 包，只通过事件观察内核（见 `AGENTS.md`「架构边界」）。
- 不要从增量里拼接最终内容去写历史或落盘；以 `message.completed` 和 `interrupted` 为准。
- 恢复会话时，`session.Load` 返回的 `State.Events` 是按同样规则重放出来的事件流，surface 用处理实时事件的同一段代码渲染它。重放的事件流里没有增量，也没有 `retrying`。
