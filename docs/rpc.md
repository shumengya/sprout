# JSON-RPC（`sprout serve`）

`sprout serve` 在 stdin / stdout 上提供 JSON-RPC 2.0 服务，供编辑器插件、脚本等程序驱动 Sprout。
实现在 `internal/rpc`，与 TUI、`sprout run` 共用同一套装配、会话记录和工具。

```
sprout serve [--data-dir DIR] [-c | --resume ID] [--effort 档位]
```

## 传输

- 一行一条消息（UTF-8 JSON，以 `\n` 结尾），不用 `Content-Length` 头。
- stdout 只输出协议消息；日志写数据目录的 `logs/`，启动失败（配置错误、`--resume` 找不到会话）写 stderr 并以退出码 1 结束。
- 不支持批量请求（数组），返回 `-32600`。
- 不带 `id` 的是通知，服务端不回响应；客户端发来的响应（只有 `result` / `error`）直接忽略。
- stdin 结束或收到 `shutdown` 时，取消进行中的一轮，最多等 5 秒让它收尾后退出，退出码 0；Ctrl+C 退出码 130。
- 字段名一律 camelCase，时长单位毫秒（字段名带 `Ms`）。

## 方法

| 方法 | 参数 | 结果 |
| --- | --- | --- |
| `initialize` | 无 | 见下 |
| `turn/start` | `{text, images?: [{path}]}` | `{turn}`：本轮编号，从 1 递增 |
| `turn/cancel` | 无 | `{cancelled}`：有一轮在进行时为 true |
| `session/new` | 无 | `{}`：开始空白会话，旧会话文件保留 |
| `session/list` | `{all?}` | `{sessions: [...]}`：当前工作目录的会话（`all` 为 true 时是全部目录），最近使用的在前 |
| `session/resume` | `{selector}` | `{id, path, notice, turns, events}`：恢复会话，`events` 是重放出的历史事件 |
| `session/rename` | `{path, name}` | `{}`：`name` 为空表示清除名字 |
| `session/delete` | `{path}` | `{}`：不能删除当前会话 |
| `session/compact` | `{focus?}` | `{tokensBefore, tokensAfter, dropped, summary}`：把全部历史总结成摘要 |
| `shutdown` | 无 | `{}`，随后退出 |

`initialize` 的结果：

```json
{
  "protocolVersion": 1, "version": "2026.11.16", "model": "deepseek/deepseek-flash",
  "provider": "deepseek", "cwd": "D:\\work", "dataDir": "D:\\sprout\\data\\.sprout",
  "tools": ["bash", "read"], "vision": false,
  "available": true, "disabled": "", "notices": []
}
```

`available` 为 false 时（缺 API Key 等）服务照常运行，`disabled` 是原因；此时 `turn/start` 与 `session/new` 返回 `-32002`。
`initialize` 不要求先调用，可以随时调用以刷新信息。

`turn/start`：

- `text` 与 `images` 不能都为空。图片只接受本地路径（相对路径以服务进程的工作目录为准），按内容嗅探类型，单张上限 3 MB；
  读不了时整个请求返回 `-32602`，不会开始这一轮。文本里写的图片路径**不会**自动附加（与 TUI / `run` 不同），程序调用方应显式放进 `images`。
- 响应先于本轮的任何事件写出。一轮进行中再调用返回 `-32001`，服务端不排队。
- 一轮结束以 `turn.finished` 事件为准；收到它之后服务端已经空闲，可以立刻开始下一轮。

会话方法：

- `session/list` 的每一项是 `{id, path, prompt, name, cwd, provider, model, updated, turns, current}`。`prompt` 是第一条提问，
  `name` 是起过的名字（没有为空），`updated` 是 RFC 3339 时间，`current` 表示正在写的会话。没有会话时是空数组。
- 重命名和删除用 `path` 定位，取 `session/list` 里的值；路径必须在数据目录的 `sessions/` 下。
- `session/resume` 的 `selector`：空串或 `latest` 是最近使用的，1～3 位数字是 `session/list`（`all` 为 false）里的序号（从 1 起），
  以 `.jsonl` 结尾的是会话文件路径（用来恢复别的目录的会话），其余按 ID 前缀匹配。
  `events` 的元素和 `event` 通知的 `params` 相同，但**不带 `turn`**：`turn` 是本进程 `turn/start` 的编号，历史轮次没有对应编号；按 `turn.started` 分轮即可。
  `notice` 说明恢复情况（换了模型时丢弃了旧的思维链、会话属于别的目录等）。
- `session/compact` 要调一次模型，**结束后**才写响应；期间算作忙碌（`turn/start`、`session/resume` 等返回 `-32001`），
  可用 `turn/cancel` 取消，取消后返回 `-32005`。压缩不是一轮对话，不推送 `event`。`focus` 是希望摘要侧重的内容。
- `session/resume`、`session/compact` 在忙碌时返回 `-32001`，不可用时返回 `-32002`；`session/list`、`rename`、`delete` 随时可调。

## 通知

**`event`**：内核事件，`params` 是 `{turn, kind, ...字段}`。`kind` 与各字段语义见 `docs/events.md`，线上字段如下：

| kind | 字段 |
| --- | --- |
| `turn.started` | `prompt`、`images: [{name, mediaType, bytes, path?, source?}]`（不含 base64） |
| `message.delta` / `reasoning.delta` | `text` |
| `message.completed` | `content`、`reasoning`、`toolCalls: [{id, name, arguments}]`、`stopReason`、`reasoningTokensEst` |
| `tool.requested` | `id`、`name`、`arguments`（原始 JSON 字符串） |
| `tool.finished` | `id`、`name`、`command`、`stdout`、`stderr`、`exitCode`、`truncated`、`timedOut`、`cancelled`、`durationMs`、`content`、`error` |
| `usage` | `inputTokens`、`outputTokens`、`totalTokens`、`reasoningTokens`、`cacheReadTokens`、`cacheWriteTokens` |
| `retrying` | `attempt`、`max`、`delayMs`、`reason` |
| `compacted` | `mode`、`tokensBefore`、`tokensAfter`、`dropped`、`summary`、`error`、`manual` |
| `interrupted` | `reason`、`content`、`reasoning`、`partial` |
| `error` | `message`、`retryable` |
| `turn.finished` | `reason`：`stop` / `cancelled` / `error` / `max_steps` / `length` |

**`notice`**：`{message}`，给用户看的提示，例如后台连接的 MCP 服务端失败。

## 错误码

| 码 | 含义 |
| --- | --- |
| `-32700` | 不是合法 JSON（`id` 为 null） |
| `-32600` | 不是合法请求：缺 `jsonrpc: "2.0"` 或 `method`、`id` 类型不对、批量请求 |
| `-32601` | 未知方法 |
| `-32602` | 参数无效 |
| `-32603` | 内部错误 |
| `-32001` | 已有一轮对话或压缩在进行 |
| `-32002` | 当前不能对话，`message` 是原因 |
| `-32003` | 会话操作失败：找不到、选择器有歧义、不能删除当前会话、路径不在 `sessions/` 下等 |
| `-32005` | 操作被 `turn/cancel` 取消（目前只有 `session/compact`） |
| `-32006` | 压缩失败：没有可压缩的对话，或生成摘要失败；历史保持原样 |

## 会话与 MCP

- 一个进程同一时刻只有一个活动会话，和 TUI 一样记录到 `sessions/` 下（格式见 `docs/session-format.md`）。
  启动时 `-c` / `--resume` 恢复会话，运行中用 `session/resume` 切换。
- MCP 在后台连接，不阻塞启动。连好之前的提问只用内置工具；连好（或服务端通知工具列表变了）之后，
  在下一次 `initialize` 或 `turn/start` 时接入，连接失败的提示放进 `initialize` 的 `notices` 或作为 `notice` 通知推送。

## 示例

```
→ {"jsonrpc":"2.0","id":1,"method":"turn/start","params":{"text":"你好"}}
← {"jsonrpc":"2.0","id":1,"result":{"turn":1}}
← {"jsonrpc":"2.0","method":"event","params":{"turn":1,"kind":"turn.started","prompt":"你好","images":[]}}
← {"jsonrpc":"2.0","method":"event","params":{"turn":1,"kind":"message.delta","text":"你好！"}}
← ...
← {"jsonrpc":"2.0","method":"event","params":{"turn":1,"kind":"turn.finished","reason":"stop"}}
```
