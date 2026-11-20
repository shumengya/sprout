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
- 不带 `id` 的是通知，服务端不回响应。客户端发来的响应（只有 `result` / `error`）按 `id` 对应到服务端发出的请求
  （见「服务端请求」）；对不上的（没问过、已取消、重复回答、数字 id）直接忽略。
- stdin 结束或收到 `shutdown` 时，取消进行中的一轮，最多等 5 秒让它收尾后退出，退出码 0；Ctrl+C 退出码 130。
- 字段名一律 camelCase，时长单位毫秒（字段名带 `Ms`）。

## 方法

| 方法 | 参数 | 结果 |
| --- | --- | --- |
| `initialize` | `{capabilities?: {elicitation?, sampling?}}` | 见下 |
| `turn/start` | `{text, images?: [{path}]}` | `{turn}`：本轮编号，从 1 递增 |
| `turn/cancel` | 无 | `{cancelled}`：有一轮在进行时为 true |
| `session/new` | 无 | `{}`：开始空白会话，旧会话文件保留 |
| `session/list` | `{all?}` | `{sessions: [...]}`：当前工作目录的会话（`all` 为 true 时是全部目录），最近使用的在前 |
| `session/resume` | `{selector}` | `{id, path, notice, turns, events}`：恢复会话，`events` 是重放出的历史事件 |
| `session/tree` | 无 | `{current, nodes: [{turn, parent, prompt, reply, images, time}]}`：当前会话的轮次树，所有分支都在同一个会话文件里 |
| `session/navigate` | `{turn}` | `{id, path, notice, turns, events}`：把当前会话的位置移到第 `turn` 轮之后（0 为会话开头），之后的提问从那里开出新分支 |
| `session/rename` | `{path, name}` | `{}`：`name` 为空表示清除名字 |
| `session/delete` | `{path}` | `{}`：不能删除当前会话 |
| `session/compact` | `{focus?, keepLast?}` | `{tokensBefore, tokensAfter, dropped, summary}`：把历史总结成摘要；`keepLast` 为 true 时保留最近一轮原文 |
| `model/list` | 无 | `{models: [{ref, provider, model, protocol, contextWindow, maxOutput, tools, vision, current}]}`，按 `ref` 排序 |
| `model/switch` | `{ref}` | `{model, effort, notice}`：切换模型，对话历史带过去 |
| `effort/list` | 无 | `{current, levels: [{name, detail}], unsupported?}` |
| `effort/set` | `{name}` | `{effort, notice}`：下一次请求生效 |
| `shutdown` | 无 | `{}`，随后退出 |

`initialize` 的结果：

```json
{
  "protocolVersion": 1, "version": "2026.11.16", "model": "deepseek/deepseek-flash",
  "provider": "deepseek", "cwd": "D:\\work", "dataDir": "D:\\sprout\\data\\.sprout",
  "tools": ["bash", "read"], "vision": false, "effort": "high",
  "available": true, "disabled": "", "notices": []
}
```

`available` 为 false 时（缺 API Key 等）服务照常运行，`disabled` 是原因；此时 `turn/start` 与 `session/new` 返回 `-32002`。
`initialize` 不要求先调用，可以随时调用以刷新信息。`capabilities` 声明客户端愿意回答哪几类服务端请求（见「服务端请求」），
每次 `initialize` 都以最新一次为准，不带或格式不对都当作什么都不支持，不会让 `initialize` 失败。

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
  `keepLast` 为 true 时只摘要最近一轮之前的对话，最近一轮（最后一条用户消息及之后）原样留在历史里，`dropped` 是被摘要替代的条数；只有一轮对话时返回 `-32006`。
- `session/tree` 的 `nodes` 按 `turn` 升序；`parent` 为 0 表示接在会话开头，`current` 是下一次提问接在第几轮之后。`reply` 是该轮最后一段文字回复，压成一行并截断；`prompt` 是原文。当前会话还没有提问时返回 `-32003`。规则见 `docs/session-format.md`「分支与会话树」。
- `session/navigate` 的 `turn` 必须显式给出（0 是合法值），缺少或为负数时返回 `-32602`；轮次不存在、当前会话还没有提问时返回 `-32003`。结果格式与 `session/resume` 相同，`events` 是新位置上重放出的历史。切换只在会话文件里追加一条 `branch` 记录，原来的分支仍可切回。要“改问”某一轮，切到它的 `parent` 再用新内容 `turn/start`。
- `session/resume`、`session/navigate`、`session/compact` 在忙碌时返回 `-32001`，`session/tree` 随时可调；这几个方法在不可用时都返回 `-32002`；`session/list`、`rename`、`delete` 随时可调。

模型与思考程度（与 TUI 的 `/model`、`/effort` 是同一套实现，切换结果会记住，下次启动沿用）：

- `model/switch` 的 `ref` 是 `provider/model`，取 `model/list` 里的值。会重建 agent，所以忙碌时返回 `-32001`。
  模型不存在返回 `-32004`；其他失败（如新模型缺 API Key）返回 `-32603`，仍用原模型。切到当前模型算成功，`notice` 说明已经在用。
  对话历史原样带过去，但丢弃旧模型的思维链；会话仍写进同一个文件。新模型有同名档位就沿用，否则用它的默认档位，结果在 `effort` 里。
- `effort`（`initialize`、`model/switch`、`effort/list` 的 `current`）在模型没有在 `models.jsonc` 声明 effort 时为空，
  此时 `effort/list` 的 `levels` 为空数组，`unsupported` 说明怎么配置。`detail` 用该协议的真实字段名描述这一档会发送什么。
- `effort/set` 的 `name` 不区分大小写。只改思考参数、不重建 agent，所以**一轮进行中也能调**，从下一次模型请求起生效。
  档位不存在或模型不支持 effort 返回 `-32004`。
- `model/list`、`effort/list` 随时可调；`model/switch`、`effort/set` 不可用时返回 `-32002`。

## 通知

**`event`**：内核事件，`params` 是 `{turn, kind, ...字段}`。`kind` 与各字段语义见 `docs/events.md`，线上字段如下：

| kind | 字段 |
| --- | --- |
| `turn.started` | `prompt`、`images: [{name, mediaType, bytes, path?, source?}]`（不含 base64） |
| `user.queued` | 同 `turn.started`。`turn/start` 不支持排队，所以实时推送里不会出现；`session/resume` 重放 TUI 里排队过的会话时会有 |
| `message.delta` / `reasoning.delta` | `text` |
| `message.completed` | `content`、`reasoning`、`toolCalls: [{id, name, arguments}]`、`stopReason`、`reasoningTokensEst` |
| `tool.requested` | `id`、`name`、`arguments`（原始 JSON 字符串） |
| `tool.finished` | `id`、`name`、`command`、`stdout`、`stderr`、`exitCode`、`truncated`、`timedOut`、`cancelled`、`durationMs`、`content`、`error`、`images: [{name, mediaType, bytes, path?}]`（不含 base64；`path` 相对会话目录） |
| `usage` | `inputTokens`、`outputTokens`、`totalTokens`、`reasoningTokens`、`cacheReadTokens`、`cacheWriteTokens` |
| `retrying` | `attempt`、`max`、`delayMs`、`reason` |
| `compacted` | `mode`、`tokensBefore`、`tokensAfter`、`dropped`、`summary`、`error`、`manual` |
| `interrupted` | `reason`、`content`、`reasoning`、`partial` |
| `error` | `message`、`retryable` |
| `turn.finished` | `reason`：`stop` / `cancelled` / `error` / `max_steps` / `length` |
| `subagent` | `id`（启动它的 `agent` 调用的 id）、`agent`（子代理类型）、`event`：子代理的一个事件，字段同本表，有 `kind` 但没有 `turn`。不推送增量；`session/resume` 重放出的只有内层为 `usage` 的这一种（见 `docs/subagents.md`） |

**`notice`**：`{message}`，给用户看的提示，例如后台连接的 MCP 服务端失败。

## 服务端请求

MCP 服务端可以在工具调用中途向用户要输入（elicitation）或请求借用模型（sampling）。TUI 里这是一个表单或确认框；
`sprout serve` 把它们作为 **JSON-RPC 请求从服务端发给客户端**，客户端回响应后工具调用才继续。

- 客户端必须在 `initialize` 的 `capabilities` 里声明对应能力（`{"elicitation": true}`、`{"sampling": true}`）。
  没声明就不会发请求：对应的 MCP 调用直接失败并把原因返回给模型，不会等待。
  MCP 服务端那边也只会在 sprout 声明了能力后才发这类请求。
- 请求的 `id` 是字符串 `srv-N`，与客户端自己的数字 `id` 不会冲突。客户端用同一个 `id` 回 `result` 或 `error`。
  收到客户端回的 `error`、连接结束（stdin 关闭或 `shutdown`），都会让等待中的请求立刻结束，不会把进行中的一轮挂住。
- 服务端请求可以与 `event` 通知交错出现，也可能同时有多个未答的请求，按 `id` 对应即可。
- 同样的请求在 `sprout run` 里不发起：那里没有人可问，服务端拿到的是“客户端不支持”。

**`mcp/elicit`**：填表。

```
← {"jsonrpc":"2.0","id":"srv-1","method":"mcp/elicit","params":{"server":"fs","message":"请填写部署参数",
    "fields":[{"name":"env","title":"环境","kind":"enum","choices":["dev","prod"],"labels":["开发","生产"],"required":true},
              {"name":"count","kind":"integer","default":3}]}}
→ {"jsonrpc":"2.0","id":"srv-1","result":{"action":"accept","values":{"env":"prod","count":3}}}
```

- `fields` 按服务端声明的顺序给出。字段 `kind` 是 `string`、`number`、`integer`、`boolean`、`enum`；
  `enum` 带 `choices`（取值）与可选的 `labels`（展示名，与 `choices` 对应）。`title`、`description`、`default` 可缺省。
  嵌套对象、数组类型的 schema 不支持，服务端会收到参数错误；URL 模式（`mode: "url"`）同样不支持。
- `action` 取 `accept`、`decline`（明确拒绝）或 `cancel`（没有表态就放弃）。只有 `accept` 带 `values`，其他情况的 `values` 被忽略。
- `accept` 时 sprout 会校验 `values`：必填字段必须有值，类型要对（`integer` 必须是整数，`enum` 必须是 `choices` 之一），
  不能出现表单里没有的字段。校验不通过当作这次请求失败，值不会交给 MCP 服务端。非必填字段可以不填。

**`mcp/sample`**：MCP 服务端请求用当前模型生成一段回复。

```
← {"jsonrpc":"2.0","id":"srv-2","method":"mcp/sample","params":{"server":"fs","message":"服务端要借用当前模型生成一段回复（最多 512 token）。\nuser：总结一下"}}
→ {"jsonrpc":"2.0","id":"srv-2","result":{"action":"accept"}}
```

- `message` 是请求内容的摘要（最大 token 数、系统提示与各条消息的首行），给用户确认用。`action` 为 `accept` 时 sprout 用当前模型（不带工具）完成请求，
  其余取值都算拒绝，MCP 服务端收到拒绝的错误。客户端应当让用户确认，不要自动同意。

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
| `-32004` | 要切换的模型或思考档位不存在 |
| `-32005` | 操作被 `turn/cancel` 取消（目前只有 `session/compact`） |
| `-32006` | 压缩失败：没有可压缩的对话（含 `keepLast` 时只有一轮），或生成摘要失败；历史保持原样 |

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
