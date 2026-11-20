# 会话格式

会话记录在 `internal/agent/session`。记录器 `Recorder` 是一个 `event.Sink`，和 surface 订阅同一条事件流，把需要的事件转成 JSONL 记录（事件本身见 `docs/events.md`）。本文以代码为准；改记录格式时同步更新本文。

## 1. 文件布局

```
<数据目录>/sessions/
  --<工作目录 slug>--/
    meta.json                         该目录分组下所有会话的索引
    <yyyymmdd-hhmmss>_<id>.jsonl      一个会话
    <yyyymmdd-hhmmss>_<id>.images/    该会话的图片：用户输入和工具结果里的（有图片时才有）
    <yyyymmdd-hhmmss>_<id>.agents/    该会话派出的子代理的记录，每次 agent 调用一个 <call id>.jsonl（有子代理时才有）
```

- **slug**（`session.Slug`）：去掉开头的 `/`、`\`，把 `/`、`\`、`:` 换成 `-`，前后加 `--`。例：`D:\SmyProjects\sprout` → `--D--SmyProjects-sprout--`，`/home/a/b` → `--home-a-b--`。
- **文件名**：会话开始时刻（本地时间，`20060102-150405`）加 8 位十六进制随机 id。恢复后继续追加不改文件名，所以会话列表按文件修改时间排序。
- **权限**：JSONL 和图片文件为 0600，目录为 0755。会话里有命令输出，可能含敏感内容。
- **子代理记录**（`session.NewChildRecorder` / `session.ChildPath`）：与普通会话格式相同，`session` 头里多一个 `agent`（子代理类型名）。文件名是启动它的 tool call id，只保留字母、数字、`-`、`_`，其余换成 `_`；同名文件已存在时不覆盖，只停止记录。不登记进 `meta.json`，所以不出现在会话列表里；子代理的图片放在 `.agents/<call id>.images/`。删除会话时 `.images/` 与 `.agents/` 一起删除。

## 2. 写入规则

- 文件懒创建：第一条需要落盘的记录到来时才创建，并先写 `session` 头记录、登记 `meta.json`。启动后什么都没做就退出不会留下空文件。
- 每条记录一行 JSON，一次 `Write` 写完，进程崩溃时最多留下最后半行。
- 写入失败只记日志、停止记录，不影响 agent 运行。`meta.json` 写失败只是警告，不影响会话记录。
- 带图片的轮次或工具结果：图片写盘失败时整条 `user` / `user_queued` / `tool` 记录不写，记录器转为失败状态，避免重放出的历史和运行时不一致。
- `Attach(path)`：恢复会话时改为向原文件追加，并写一条 `resume` 记录。
- `Reset(info)`：`/new` 时结束当前文件，下一条记录写进新会话；`info` 换成当前模型。
- `Fork(dir, src, turns)`：分支会话，见第 6 节。
- `NoteModelChange`：会话中途切换模型时写一条 `resume` 记录；还没有文件时不写。

## 3. 记录类型

每条记录都有 `type` 和 `time`（RFC 3339）。下表列出其余字段；标了“省略”的字段为零值时不写。

| type | 来源 | 字段 |
| --- | --- | --- |
| `session` | 文件创建时 | `id`、`cwd`、`provider`、`model`、`tools`、`version`（省略）、`agent`（省略；只有子代理记录才有） |
| `user` | `turn.started` | `content`、`images`（省略） |
| `user_queued` | `user.queued` | 同 `user`；一轮中途接进来的排队输入，属于当前这一轮 |
| `assistant` | `message.completed` | `content`、`reasoning`（省略）、`tool_calls: [{id, name, arguments}]`（省略）、`stop_reason`（省略）、`native`（省略） |
| `tool` | `tool.finished` | `tool_call_id`、`name`、`command`、`stdout`、`stderr`、`exit_code`、`truncated`、`timed_out`、`cancelled`、`duration_ms`、`error`、`content`、`images`；除 `tool_call_id`、`name`、`exit_code`、`duration_ms` 外都省略零值 |
| `usage` | `usage` | `input_tokens`、`output_tokens`、`total_tokens`、`reasoning_tokens`（省略）、`cache_read_tokens`（省略）、`cache_write_tokens`（省略） |
| `subagent_usage` | 内层为 `usage` 的 `subagent` | `tool_call_id`、`agent`，其余字段同 `usage`；写在那次 `agent` 调用的 `tool` 记录之前 |
| `compact` | `compacted` | `mode`、`tokens_before`、`tokens_after`、`dropped`、`summary`（省略）、`error`（省略）、`manual`（省略）、`prune_version`（省略） |
| `interrupted` | `interrupted` | `reason`、`content`、`reasoning`（省略） |
| `error` | `error` | `message`、`retryable`（省略） |
| `turn` | `turn.finished` | `reason` |
| `retry` | `retrying` | `attempt`、`max`、`delay_ms`、`reason`；仅审计 |
| `branch` | `/tree` 切换位置 | `turn`：之后的记录接在第几轮之后，0 是会话开头（见第 6 节） |
| `resume` | `Attach` / `NoteModelChange` | `provider`、`model`、`tools`；仅审计 |

- `exit_code` 不省略，因为 0 是有意义的值。
- `user`、`user_queued` 和 `tool` 的 `images` 格式相同，每项为 `{file, media_type, bytes, name?, source?}`。`file` 是相对会话目录的路径（如 `20261116-101500_ab12cd34.images/0123456789abcdef.png`），文件名取内容 SHA-256 的前 8 字节加扩展名，同一张图重复发只存一份。JSONL 里不内联 base64。
- `native` 是 `{protocol, model, data}`，`data` 是该协议的原始 JSON，内核不解析。
- `message.delta`、`reasoning.delta`、`tool.requested` 不落盘。内层不是 `usage` 的 `subagent` 也不落进主会话，子代理的完整过程在它自己的记录文件里。
- 旧会话缺少的字段（`reasoning_tokens`、缓存两项、`manual`、`images` 的 `name` / `source` 等）读出来是零值，按旧语义处理。

## 4. meta.json

```json
{
  "cwd": "D:\\SmyProjects\\sprout",
  "sessions": {
    "20261116-101500_ab12cd34.jsonl": {
      "id": "ab12cd34", "provider": "deepseek", "model": "deepseek-flash",
      "tools": ["bash", "powershell"], "startedAt": "2026-11-16T10:15:00+08:00",
      "version": "2026.11.14", "name": "用户起的名字"
    }
  }
}
```

- 它是同一工作目录分组下所有会话的索引，键是 JSONL 文件名。各会话 JSONL 开头的 `session` 记录才是权威来源；唯一只存在这里的是 `/resume` 里起的 `name`。
- 读-改-写后用临时文件加改名原子替换。损坏时重建，并记一条警告。
- 不加跨进程锁：两个实例同时在同一分组下创建会话，可能丢一条登记，JSONL 本身不受影响。

## 5. 恢复（`session.Load`）

`Load` 重放文件末尾所在分支（从根到当前位置的路径，见第 6 节；没有 `branch` 记录的文件就是逐行重放），得到 agent 的 `History` 和 `Summary`、给 surface 渲染用的 `Events`、轮数 `Turns`，以及跳过的坏行数 `Skipped`。重放规则和运行时一致，由“黄金对照”测试（`golden_test.go`）保证：

- 并行工具的结果按调用顺序排进历史，不按完成顺序。
- 进程被杀留下的未完成 tool call，补一条“未执行”的结果。
- 首次模型调用就失败或被取消、只留下 `user` 的旧式轮次，撤销那条用户消息（当年运行时就是这么做的）。新记录有 `interrupted`，不触发这条规则。
- 以 `内部错误` 开头的 `error`（panic）让历史还原到本轮开始前。
- `compact` 按当初的方式重做：summary 丢弃前 `dropped` 条消息并换上摘要（手动压缩保留最近一轮时 `dropped` 小于历史条数，留下的就是那一轮）；prune 用纯函数 `agent.PruneToolOutputs` 按 `prune_version` 的策略从最旧的开始裁同样多条。没有 `prune_version` 的旧记录按版本 1（统一保留开头 200 字符）裁，与当年的结果一致。
- `interrupted` 还原成一条 assistant 消息，内容与当初写进历史的一致。
- `user_queued` 先把上一批工具结果按调用顺序补齐，再追加一条用户消息（运行时它就接在整批结果之后），不开始新的一轮，不计入 `Turns`，也不参与第 6 节的轮号编号。旧版本程序读到它会当作未知类型忽略，恢复出的历史少这条消息，但仍然合法。
- `subagent_usage` 还原成内层为 `Usage` 的 `event.Subagent`，不动历史（它夹在 `agent` 调用的 `assistant` 与 `tool` 记录之间）。旧版本程序读到它会当作未知类型忽略，只是恢复后的花费少算子代理那部分。
- 图片按 `file` 读回，`tool` 记录的图片跟在结果文本后面（与运行时的 tool 消息顺序一致）；文件丢失或损坏时换成“（图片无法恢复：路径）”文本块，不让整轮无法恢复。
- 无法解析的行跳过并计数；`retry`、`resume` 和未知类型只用于审计，重放时忽略；`branch` 只决定取哪条路径（第 6 节），本身不进历史。
- 换了模型或服务商时，调用方用 `State.StripReasoning` 清掉旧思维链和 `native`。

## 6. 分支与会话树（`session.ReadTree` / `session.LoadAt`）

一个会话的所有分支都在同一个 JSONL 文件里，形成一棵以“轮”为节点的树。TUI 的 `/tree` 与 RPC 的 `session/tree`、`session/navigate` 用它在分支之间切换。

- **轮号**：`user` 记录在文件里按出现顺序从 1 起编号，跨分支统一编号。
- **接续规则**：一条记录默认接在上一条记录所在的位置后面。`branch` 记录 `{"turn": N}` 把位置移到第 N 轮之后（0 是会话开头），之后的记录（下一条 `user`，以及切换后、提问前的 `compact`、`resume` 等）都接在那里。`branch` 记录指向尚未出现的轮次或负数时视为坏行，计入 `Skipped`，位置不动。
- **重放某个位置**：只取从根到该位置路径上的记录，按文件顺序交给第 5 节的重放规则。JSONL 前缀重放出来就是当时的状态，路径上的记录正是“当时”的那个前缀，所以 `compact` 只影响它所在分支。
- **当前位置**：文件末尾所在的位置。`Load`（`/resume`、启动恢复）重放的就是这里，所以切到某个分支后退出再恢复，回到的仍是那个分支。
- **切换**：先用 `LoadAt` 在不改文件的情况下重放目标位置，成功后再追加 `branch` 记录，最后替换 agent 的历史；任何一步失败，文件和 agent 都保持原样。切换只追加记录，不复制、不删除任何内容，图片仍在原会话的 `.images/` 目录里。
- **兼容性**：没有 `branch` 记录的旧文件就是一条链，重放结果与以前逐行重放完全相同。旧版本程序读到 `branch` 记录会当作未知类型忽略，按逐行重放处理（分支会被拼在一起）。
- **轮次树**：`ReadTree` 给出每一轮的 `Turn`、`Parent`（接在第几轮之后，0 为开头）、提问原文、最后一段文字回复（压成一行并截断）、图片数和时间，以及 `Current`（下一次提问接在第几轮之后）。

