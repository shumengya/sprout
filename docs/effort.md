# 思考程度（/effort）

让用户按需调节模型的思考深度：`off / low / medium / high / xhigh / max` 等档位，TUI 里用 `/effort` 切换，
命令行用 `--effort`。思考参数按协议各自的真实字段发送，模型能接受什么由 `models.jsonc` 声明，代码不按模型名猜。

## 1. 设计取向

- **各家参数不同构，不做统一映射表**。OpenAI 是强度枚举，Gemini 2.5 和老 Claude 是 token 预算，
  Gemini 3 改成 `thinkingLevel`，Claude 4.6 起是 `adaptive + output_config.effort`（新模型已拒收 `budget_tokens`），
  DeepSeek 关思考还要另发私有开关。写死映射表要么发错，要么跟不上新模型，所以每个档位要发什么由配置写明。
- **没声明就不发**。没写 `effort` 的模型请求与以前逐字节一致；声明了但处在 `auto` 档位时也一样。
  不会因为加了这个功能，给不支持思考参数的服务（如 gpt-4o-mini）发出会被拒绝的字段。
- **档位是会话之外的运行状态**。跨 `/new`、`/resume` 保留；切模型时新模型有同名档位就沿用，没有就退回它的默认档位。
  不写进会话文件：恢复旧会话不应悄悄改变当前档位。
- **调整不重建 agent**。档位只影响下一次请求的参数，生成中也能改，从下一次模型调用开始生效。

## 2. 配置（`models.jsonc`）

`effort` 写在模型下面：

```jsonc
"deepseek-flash": {
  "effort": {
    "default": "high",      // 可选，启动档位；不写为 auto
    "summary": true,        // 可选，是否要求返回可读的思考摘要，缺省 true
    "levels": {
      "off":  { "body": { "thinking": { "type": "disabled" } } },
      "low":  "low",        // 字符串：强度枚举
      "high": "high",
      "max":  "max"
    }
  }
}
```

- `levels` 的写法：
  - 数组 `["low", "high"]`：档位名就是强度取值；`off` 表示不发任何参数。
  - 对象，每个档位可以是 `null`（不发参数）、字符串（强度）、整数（token 预算）、
    或 `{ "effort", "budget", "body" }`。`body` 合并进请求体顶层，同名字段覆盖适配器写的值。
- 档位名只能取 `off minimal low medium high xhigh max`，界面按这个顺序从弱到强排列，与书写顺序无关。
- `auto` 是保留档位，永远可选且排在最前：什么都不发，由服务端按模型默认处理。不能在 `levels` 里声明。
- 加载时校验（一次报全）：未知档位名、声明 `auto`、空 `levels`、`default` 不在 `levels` 里、
  OpenAI 协议写了预算、Anthropic 预算小于 1024 或不小于 `maxOutput`、Gemini 预算小于 -1、
  Gemini 同时写强度和预算、对象里出现未知字段（如 `budgets`、`efort` 这类笔误）。

模板（`models.example.jsonc`）给四种协议各示范了一种写法。

## 3. 各协议发送什么

内核只认中性的 `agent.Thinking`（`Effort` 字符串、`Budget *int`、`Summary`、`Extra`），零值表示不发。
摘要请求（上下文压缩）不带思考参数。

| 协议 | 强度 `effort` | 预算 `budget` | `summary` | 思考内容的返回 |
| --- | --- | --- | --- | --- |
| openai-chat | `reasoning_effort` | 配置校验拒绝 | 无对应参数 | `reasoning_content`（DeepSeek 等） |
| openai-responses | `reasoning.effort` | 配置校验拒绝 | `reasoning.summary: "auto"` | `response.reasoning_summary_text.delta`、`response.reasoning_text.delta`；没有增量时用 `reasoning` 条目的 `summary` 补 |
| anthropic-messages | `thinking: {type: "adaptive"}` + `output_config.effort` | `> 0`：`thinking: {type: "enabled", budget_tokens}`；`0`：`{type: "disabled"}` | adaptive 时 `display: "summarized"` | `thinking_delta`；签名见 §4 |
| google-generate | `thinkingConfig.thinkingLevel` | `thinkingConfig.thinkingBudget`（0 关闭，-1 动态） | `includeThoughts: true`（预算为 0 时不发） | `thought: true` 的 part |

Anthropic 预算模式下 `budget_tokens` 必须小于 `max_tokens`：配置了 `maxOutput` 时由校验保证；
没配置时默认上限可能更小，就把 `max_tokens` 抬到「预算 + 4096」，而不是偷偷缩小预算。

`body` 走共享传输层：`transport.RequestOptions.Extra` 在编码时合并到请求体顶层（按键排序，结果稳定）；
没有额外字段时与直接 `json.Marshal` 逐字节一致。

## 4. Anthropic 思考块回传（`Message.Native`）

开启思考后，Anthropic 要求带 `tool_use` 的 assistant 消息连同原始 thinking block（含服务端签名）回传，
缺了会被拒绝；`redacted_thinking` 的加密内容也必须原样带回。内部消息模型只有一个 `Reasoning` 字符串，表达不了这些。

- 新增 `event.Native{Protocol, Model, Data}`（`agent.Native` 是它的别名）：provider 返回的原始 assistant 内容。
  内核只负责保存、随 `MessageCompleted` 发出、落盘，以及换模型时清掉，不解析 `Data`。
- Anthropic 适配器在流里按 block index 累积 `thinking`（文本 + `signature_delta`）、`redacted_thinking`、`text`、`tool_use`，
  结束后按顺序拼成原始块数组。**只有出现了思考块才保存**；有本适配器复现不了的块（服务端工具等）或工具参数不是合法 JSON 时
  也不保存，回退成按内部消息重建——宁可丢掉思考块，也不发出服务端无法接受的内容。空的 text 块不回传。
- 构造请求时，`Native.Usable(protocol, model)` 为真（同一协议、同一模型）就原样回传整段内容，
  其中的 `tool_use` id 登记进配对表，后面的工具结果照常配对；否则按内部消息重建，不带思考块。
- `Message.StripReasoning()` 同时清掉 `Reasoning` 和 `Native`。切模型、恢复时模型不同都走它。
- 签名还绑定了产生它时的完整前缀（系统提示词、工具、之前的全部消息），改过前缀再回传会被拒绝（新账号 400）。
  所以这几种情况用 `agent.DropNative` 清掉全部原始内容，只保留思维链文本：裁剪工具输出（改了中段）、
  摘要压缩（保留的轮次前面换成了摘要）、恢复会话（新进程的系统提示词日期可能不同）。会话重放与运行时做同样的处理。

## 5. 会话格式

`assistant` 记录新增可选字段 `native`，只在有原始内容时出现：

```json
{"type":"assistant","content":"我看看","tool_calls":[...],
 "native":{"protocol":"anthropic-messages","model":"claude-opus-5-5",
           "data":[{"type":"thinking","thinking":"…","signature":"…"},{"type":"text","text":"我看看"},{"type":"tool_use",…}]}}
```

恢复会话时重放进 `Message.Native`；模型不同则随思维链一起丢弃。旧会话没有这个字段，行为不变。
档位本身不落盘（见 §1）。

## 6. 界面与命令行

- `/effort`：在输入框位置打开横向档位条，`← →`（或 `h l`、`Tab`）移动，`1-9` 直接跳到第 n 个档位，
  `Enter` 确认，`Esc` 取消。下方一行用该协议的真实字段名说明选中档位会发送什么（如 `output_config.effort=max`）。
- `/effort 档位`：直接切换，大小写不敏感。生成中也可以用。
- 当前模型没有声明 `effort` 时，`/effort` 说明原因和配置方法，不打开档位条。
- 状态栏第二行在模型ID后显示 `思考强度 high`；没声明 `effort` 的模型不显示。
- 命令行 `--effort 档位`（TUI 与 `run` 都支持）覆盖 `default`；档位不存在或模型没声明 `effort` 时启动报错，
  而不是静默忽略。

## 7. 未验证

- 所有协议的请求与流解析只有 `httptest` 测试，没有打真实服务。
- DeepSeek 三个入口的写法依据其「思考模式」文档；Responses 入口是否接受 `reasoning.summary` 未确认，模板里关掉了。
- Gemini 3 的 `thoughtSignature` 会随带签名的 part 存进 `Message.Native`，下一轮原样回传（见 `internal/provider/google`）。
  纯思考摘要（没有签名的 `thought` part）不回传，相邻的逐字文本会被合并成一个 part。
- Responses 协议开启思考时请求 `include: ["reasoning.encrypted_content"]`，把流里的 reasoning 条目原文存进
  `Message.Native`，下一轮放在同一条 assistant 的正文与 `function_call` 之前回传。
  换模型、被压缩裁剪后随其他 `Native` 一起清掉。
