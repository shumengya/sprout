# MCP

Sprout 内置一个最小化的 MCP（Model Context Protocol）客户端，把外部服务端提供的工具注册给模型，名字统一是 `mcp__<服务端>__<工具>`。

协议参考：[modelcontextprotocol.io](https://modelcontextprotocol.io)。当前规范版本 `2026-07-28`，从这一版起协议改为无状态核心（没有 `initialize` 握手、没有 `Mcp-Session-Id`）。

## 为什么自己实现

内置的四个模型协议适配器都是手写的，MCP 工具部分需要的只是 JSON-RPC 2.0 的收发和两个方法（`tools/list`、`tools/call`）。所以 `internal/agent/mcp` 自己写了两种传输，合计约 900 行，零新增依赖。

这是一次成本判断的结果，不是禁令。判断规则见 `AGENTS.md`「架构边界」：硬约束只有「纯 Go、无 CGO、可交叉编译」，其余看引入后 `go.mod` 实际增加的间接依赖、上游成熟度、与自己实现的工作量对比。

具体到 OAuth，有个容易算错的点：MCP **客户端**只需要 PKCE、授权服务器元数据发现和令牌存储，全程标准库就能写（`crypto/rand`、`crypto/sha256`、`net/http` 等）。**不需要 JWT / JWKS 校验**——校验令牌签名是资源服务端的职责，客户端拿到的只是一段不透明的 bearer token，原样放进 `Authorization` 头即可。所以「引 OAuth 会带进 JWT 依赖」不是客户端的真实成本。

真正该记的顾虑是规范漂移：2026-07-28 这一版把动态客户端注册（RFC 7591）降级、改为推荐 Client ID Metadata Documents（CIMD），并加了 issuer 校验。无论自写还是引 SDK，这块都要跟着规范改。官方 Go SDK 的客户端 OAuth 目前仍标注 experimental，因此「引 SDK 更省事」在这个功能上未必成立。

## 配置：`mcp.jsonc`

放在数据目录（默认 `<exeDir>/data/.sprout/mcp.jsonc`），首次启动生成一份全部注释掉的模板。JSONC：支持 `//`、`/* */` 与尾逗号，与 `models.jsonc` 同一套解析和报错定位。`examples/mcp.example.jsonc` 与内置模板逐字一致，由测试保证。

```jsonc
{
  "servers": {
    // 远程：走 Streamable HTTP
    "tavily": {
      "enable": true,
      "url": "https://mcp.example.com/mcp",
      "headers": { "Authorization": "Bearer ${TAVILY_API_KEY}" },
      "timeoutSeconds": 60,
      "excludeTools": ["tavily_crawl"]
    },

    // 本地：以子进程方式启动，走 stdio
    "filesystem": {
      "enable": true,
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "D:/work"],
      "env": { "API_KEY": "${MY_API_KEY}" },
      "cwd": ""
    }
  }
}
```

| 字段 | 说明 |
| --- | --- |
| `enable` | 必须显式写 `true` 才连接 |
| `url` | 远程端点，走 Streamable HTTP；与 `command` 二选一 |
| `headers` | 远程服务端的请求头 |
| `command` / `args` / `env` / `cwd` | 本地服务端。`command` 不经 shell，含空格会被拒绝（请写程序路径 + `args`） |
| `timeoutSeconds` | 单次请求超时，默认 60，最大 600 |
| `excludeTools` | 不注册的工具（服务端上的原始名字，大小写不敏感） |
| `oauth` | 远程服务端用 OAuth 登录，`{}` 即可；可选 `clientId`（支持 `${VAR}`）、`scopes`、`callbackPort`（0–65535，0 表示随机端口）。只能用在 `url` 服务端上，见「OAuth」一节 |

`${VAR}` 会展开成当前进程的环境变量（`url`、`args`、`env`、`headers` 里的值），用来避免把令牌明文写进配置文件。展开发生在装载阶段，启动后改环境变量不影响已连接的会话。

配置缺失视为“没有 MCP 服务端”，不是错误；字段写错（同时写了 `url` 与 `command`、启用了却没写地址、URL 不是 http/https、服务端名不合法）会在启动时报出全部问题并退出。

## 协议与传输

### 握手

`sprout` 先按新规范调 `server/discover`，`_meta` 里带 `io.modelcontextprotocol/protocolVersion: "2026-07-28"`、`clientInfo`、`clientCapabilities`。服务端支持的版本里包含 `2026-07-28` 就进入**无状态模式**：后续每个请求都在 `_meta` 里重复这三项，HTTP 请求另外带 `Mcp-Method`、`Mcp-Name`、`MCP-Protocol-Version` 头。

否则（大多数现网服务端）退回旧版：`initialize` 声明 `2025-06-18`，接受服务端谈成的任何版本，再发 `notifications/initialized`。此时 HTTP 请求带 `Mcp-Session-Id`（服务端下发时）与协商出的 `MCP-Protocol-Version`。

退回前会清掉刚才试 `server/discover` 时留下的版本头——旧版服务端会因为不认识的版本号直接拒掉 `initialize`。

### 传输

| 传输 | 实现 |
| --- | --- |
| stdio | `exec.CommandContext` 启动子进程，stdout 每行一条 JSON-RPC 消息，stderr 只保留末尾 4KB 用于报错时回显；关 stdin → 等 2 秒 → 终止整个进程树（复用 `platform.ConfigureCmd`） |
| Streamable HTTP | 每个请求一次 POST，`Accept: application/json, text/event-stream`；响应是 JSON 或 SSE 都支持，SSE 里跳过通知、按 id 匹配真正的响应；关闭时尽量发 DELETE 结束会话 |

stdio 的读循环按 id 分发响应到等待者，所以同一批工具调用可以并发。服务端发来的 `ping`、`roots/list` 会随手回掉，其余请求回“方法不支持”，避免服务端干等。

### 工具

`tools/list` 会跟着 `nextCursor` 翻页（最多 20 页，防止环状 cursor）。工具名清洗后拼上前缀：`mcp__` + 服务端名 + `__` + 原始工具名，只保留字母、数字、`_`、`-`，字母开头，最长 64 字符（`mcp.ToolName`）。

`tools/call` 的结果按内容类型转换：`text` 直接拼接；`image` 校验后作为图片块跟在 tool 消息的文本后面交给模型，文本里留一行 `[图片 1：image/png，已附在结果后]`。校验要求 base64 能解开、解码后不超过 3 MB、内容嗅探是 png / jpeg / gif / webp（类型以嗅探为准，不信 `mimeType`），不合格的只写 `[图片内容：image/png，未附上：原因]`。模型不支持识图时，图片块和用户输入的图片一样换成占位文本。音频、资源、资源链接仍写成占位；`structuredContent` 附在后面；`isError: true` 映射成退出码 1，让模型看到失败但不会中断 agent。

冲突：与内置工具、外部工具、其他 MCP 工具重名时先注册的赢（内置 > 外部 > MCP），被跳过的那条写在 `/tools` 的不可用列表里。

### 资源

服务端在握手时声明了 `resources` 能力，才会注册两个资源工具（`mcp.ResourceTools`）；没声明的不注册，模型不会白调一次才知道没有。名字同样走 `mcp.ToolName`：

| 工具 | 作用 |
| --- | --- |
| `mcp__<服务端>__list_resources` | 调 `resources/list` 与 `resources/templates/list`，列出资源与模板。模板的 `uriTemplate` 里有 `{参数}`，要先填好再读 |
| `mcp__<服务端>__read_resource` | 调 `resources/read`，参数 `{"uri": "..."}` |

两个方法都跟着 `nextCursor` 翻页（最多 20 页）。服务端只实现了其中一个（另一个回 `-32601` 方法不存在）时，那一半当作空列表。

`resources/read` 可能返回多段内容：文本原样输出，多段时每段前加 `--- uri ---`；二进制内容（`blob`，base64）按与工具结果图片相同的规则校验（格式、大小），合格的图片作为图片块附在结果后，不合格或不是图片的只在文本里说明字节数和原因，不把 base64 原文塞给模型。

这两个工具不进 `excludeTools`：服务端的工具列表里没有这两个名字，面板里也就看不到它们。不支持 `resources/subscribe`，资源列表的变更通知也不处理。

### 提示词（Prompts）

服务端声明了 `prompts` 能力才注册（`mcp.PromptTools`），规则同资源工具：

| 工具 | 作用 |
| --- | --- |
| `mcp__<服务端>__list_prompts` | 调 `prompts/list`（跟着 `nextCursor` 翻页），列出模板名、标题、说明和参数（必填 / 可选） |
| `mcp__<服务端>__get_prompt` | 调 `prompts/get`，参数 `{"name": "...", "arguments": {"键": "字符串值"}}`，返回展开后的消息，每条标出角色；图片按工具结果图片的规则校验后附上 |

提示词作为工具交给模型，不做成斜杠命令；`/技能名` 只对应 Agent Skills。

### 服务端向用户要输入：Elicitation 与 Sampling

两种请求都由界面层处理（`Spec.Elicit` / `Spec.Sample`），**只有接了处理函数时握手才声明对应的客户端能力**，规范要求服务端不得使用未声明的能力：

| 入口 | elicitation | sampling |
| --- | --- | --- |
| TUI | 底部弹表单 | 弹确认框，同意后用当前模型生成 |
| `sprout serve` | 客户端在 `initialize` 声明了才转发（`mcp/elicit`，见 `docs/rpc.md`） | 同左（`mcp/sample`） |
| `sprout run` | 不声明 | 不声明 |

- **Elicitation**（`elicitation/create`）：只支持 `form` 模式，`url` 模式回 `-32602`。`requestedSchema` 必须是扁平对象，属性只能是 `string`、`number`、`integer`、`boolean` 与枚举，嵌套或未知类型直接拒绝；字段按 JSON 里的顺序排列（服务端有意安排的先后）。用户的回答是 `accept`（带内容）、`decline` 或 `cancel`。
- **Sampling**（`sampling/createMessage`）：服务端借用的是用户的模型和额度，不能静默发生。确认框显示是哪个服务端、最多生成多少 token、服务端建议的模型，以及系统提示和每条消息的首行；拒绝时回错误码 `-1`。同意后用**当前模型**生成，服务端的 `modelPreferences` 只做展示，不切换模型。图片、音频等非文本内容换成占位文字。
- 等用户回答的时间不计入请求超时：输入期间暂停该客户端所有进行中请求的计时。
- 界面已退出、等待队列已满或上下文取消时立即拒绝，不堵住 MCP 的读循环。

旧版协议里这两种请求是服务端发来的 JSON-RPC 请求（stdio 读循环、HTTP 的 SSE 流里收到，回复用 POST 发回）；`ping`、`roots/list` 仍随手回掉，其他方法回 `-32601`。

### 多轮请求（MRTR）

2026-07-28 协议把服务端的输入请求改成结果里的 `resultType: "input_required"`。`tools/call`、`resources/read`、`prompts/get` 都走同一个循环（`Client.rpc`）：

1. 结果是 `input_required` 时，按键名顺序逐个处理 `inputRequests`（与上面的 elicitation / sampling 共用处理逻辑）；
2. 用**新请求**重发原参数，附上 `inputResponses`，服务端给了 `requestState` 时原样回传；
3. 直到拿到最终结果。连续超过 8 轮（`maxInputRounds`）放弃并报错，防止服务端无限追问。

任何一项输入失败（用户拒绝采样、客户端没声明能力）都让这次调用失败，错误文本交给模型。

### OAuth

远程服务端需要登录时，在配置里加 `"oauth": {}`，然后在终端执行一次：

```bash
sprout mcp login github     # 打开浏览器授权，令牌存进数据目录
sprout mcp logout github    # 删掉保存的令牌（客户端注册信息一并删除）
```

登录流程（`mcp.Login`）：

1. **探测**：不带令牌 POST 一次 MCP 端点，必须得到 401；从 `WWW-Authenticate` 里取 `resource_metadata` 与 `scope`。服务端不要求认证时报“不需要登录”。
2. **受保护资源元数据**（RFC 9728）：优先用 `resource_metadata` 给的地址，否则按 well-known 路径找（先带 URL 路径，再不带）。`resource` 必须等于配置里的 URL，防止被别家服务端冒用。
3. **授权服务器元数据**：依次试 `oauth-authorization-server` 与 `openid-configuration`；`issuer` 必须与取文档的地址一致，`code_challenge_methods_supported` 必须含 `S256`，否则拒绝登录。
4. **客户端 ID**：配置的 `clientId` > 上次登录注册的（同一 issuer）> 动态注册（RFC 7591，公开客户端，`token_endpoint_auth_method: none`）。不能动态注册又没配 `clientId` 时报错，提示去服务商那里建应用。不支持 Client ID Metadata Document。
5. **授权**：PKCE S256 + 32 字节随机 `state`，`resource` 参数指向 MCP 端点；scope 取配置的 `scopes`，没有时用 401 里的 `scope`。回调地址是 `http://127.0.0.1:<端口>/callback`（`callbackPort` 为 0 时随机端口；有些服务商要求预先登记回调地址，这时固定一个端口）。浏览器打不开时把地址打印出来让用户手动复制。等待 5 分钟，Ctrl+C 取消。
6. **回调校验**：`state` 不对或 `Host` 不是回环地址的请求回 400，但**不结束登录**（防止别的网页抢先打一个错误请求把登录搅掉）。授权服务器声明了 RFC 9207 时 `iss` 必须带且一致；没声明但带了，也必须一致。
7. **换令牌**：`token_type` 必须是 Bearer。授权、令牌、注册端点都必须是 https，或回环地址的 http。

令牌存在 `<数据目录>/mcp-auth.json`（权限 0600，先写临时文件再改名），按服务端名分组并记下登录时的 URL：配置里的 URL 改了，旧令牌不会发给新地址，需要重新登录。文件损坏时报错，不覆盖写入。令牌不进日志，也不进 `mcp-versions.json`。

连接时（`mcp.TokenSource`）：

- 令牌离过期不足 1 分钟就用 `refresh_token` 刷新，同一服务端的并发请求只刷新一次；服务端没轮换 `refresh_token` 时沿用旧的。
- 刷新得到 `invalid_grant`：清掉令牌、保留客户端 ID，提示运行 `sprout mcp login <名字>`。
- 还没登录时不发请求，直接报需要登录。
- `headers` 里手写的 `Authorization` 优先于 OAuth 令牌。

认证失败的提示：

| 情况 | 提示 |
| --- | --- |
| 配了 `oauth`，服务端回 401 | 令牌被拒，重新运行 `sprout mcp login <名字>` |
| 没配 `oauth`，服务端回 401 且要求 Bearer | 在配置里加 `"oauth": {}` 后登录 |
| 配了 `oauth`，403 `insufficient_scope` | 令牌权限不足，重新运行 `sprout mcp login <名字>`（需要的 scope 写进配置的 `scopes`） |

登录在独立命令里完成，不在 TUI 里弹浏览器；正在运行的 sprout 登录后在 `/mcp` 面板按 `r` 重连即可。

## 启动与生命周期

- 只连接 `enable: true` 的服务端；模型 `tools=false` 时完全不连——注册了也发不下去。
- **连接在后台进行**：启动时立即显示界面，状态栏的 MCP 一项显示 `连接中`；连好后工具自动接进 agent，界面里的对话历史和上下文摘要原样保留，不需要重启。生成中连好的会等到这一轮结束再接，避免重建 agent 时丢掉正在进行的消息。
- 这期间提问也能用，只是暂时没有 MCP 工具；内置工具照常工作。
- 所有服务端并行连接，整体上限 20 秒；单个服务端按自己的 `timeoutSeconds` 超时。任一个失败都不阻止 sprout 启动，失败原因出现在对话区顶部提示、`/tools` 与状态栏。
- `sprout run` 是单次提问，会先等连接结束再问，保证这一次能用上 MCP 工具。
- 启动后改 `mcp.jsonc`（地址、命令、开关）在 TUI 里按 `/mcp` 面板的 `r` 重连即可生效，不必重启；`sprout run` 每次启动都会重新读配置，同样不必手动重启。
- 退出时关闭全部连接（stdio 终止子进程，HTTP 尽力 DELETE）。

### 协议版本缓存

每个服务端上次谈成的协议版本记在 `<数据目录>/mcp-versions.json`，下次启动直接按它握手，省掉一轮注定失败的 `server/discover` 试探（现网服务端几乎都只认旧版握手）。缓存不是权威：连接目标（地址或命令 + 参数）变了就自动作废；按缓存握手失败时退回完整协商。缓存里存的是目标指纹的 SHA-256，不写地址原文，避免 `${VAR}` 展开出的令牌落盘。删掉这个文件没有副作用，只是下次多一次往返。

## 界面

- 状态栏第三行“MCP”显示 `已连接/已启用`，例如 `2/3`（分母只算启用的服务端）；连接进行中显示 `连接中`；一个都没启用时显示 `—`。
- `/tools` 列出每个服务端：工具数、地址或命令、协商出的协议版本、连接失败原因、是否停用。
- `/mcp` 打开服务端面板，见下一节。
- `sprout run` 把连接失败写到 stderr。

## `/mcp` 面板

在 TUI 里输入 `/mcp` 打开。面板替换输入框，列出全部已配置的服务端（含停用的）以及展开后的工具：

```
MCP 服务端                                                    已启用 1/2
↑↓ 选择 · 空格 / Enter 开关 · → 展开 · ← 收起 · r 刷新 · Esc 关闭
❯ ▾ ● github                                                      1/2 个工具
    ●   search  搜索仓库
    ○   write_file  写文件
  ▾ − fs  已停用
(1/4)
```

| 按键 | 作用 |
| --- | --- |
| `↑` `↓`（或 `Ctrl+P` / `Ctrl+N` / `j` / `k`） | 在服务端行与工具行之间移动 |
| `PgUp` `PgDn` `Home` `End` | 快速翻页 / 跳到首尾 |
| `→` / `l` | 展开当前位置的服务端 |
| `←` / `h` | 收起；在工具行上按会连同服务端一起收起并回到服务端行 |
| `空格` 或 `Enter` | 开关当前位置：服务端切换 `enable`，工具切换是否交给模型 |
| `r` | 重读 `mcp.jsonc`、断开并重连全部已启用的服务端 |
| `Esc` | 关闭面板 |

行首标记：`●` 已连接 / 工具已启用，`○` 工具已屏蔽，`−` 服务端已停用，`◌` 连接中，`✗` 连接失败。

行为：

- **开关直接写回 `mcp.jsonc`**：服务端写 `enable`，工具写 `excludeTools`。写入保留原有的注释、缩进和字段顺序（只替换那个值，缺键时按同层缩进插一行），不重新序列化整个文件。改完会重新解析并跑一次校验；改动让配置不合法时（例如启用一个既没写 `url` 也没写 `command` 的服务端）回滚并报错，不留下坏配置。
- **工具开关与 `excludeTools` 是同一份数据**：在面板里打开曾被 `excludeTools` 屏蔽的工具，就是把名字从列表里删掉；列表清空后写回 `[]`，键保留。因此面板和手改配置不会各说各话。
- **服务端开关**：停用会断开连接并从 agent 的注册表里移除它的工具；启用会连接该服务端，失败时 `enable: true` 仍留在配置里（状态记为“连接失败”），修好网络后按 `r` 重试即可。
- **工具开关不重连**：服务端的工具列表没变，只按新的 `excludeTools` 重新注册。
- **重建 agent**：任何开关生效后都要重建 agent 才能让模型看到新的工具集，这一步由 TUI 在**空闲时**的收尾执行（和后台连接完成时一样），保留对话历史与摘要。所以面板只能在空闲时打开；生成、压缩、恢复会话期间 `/mcp` 会被拒绝。一次面板操作进行中（连接可能要几秒）再按开关会被忽略，避免并发改同一份配置。
- 当前模型 `tools=false` 时不能启用服务端（注册了也发不出去）。
- 停在工具行上按空格时，会同时写入配置并重建注册表，不需要再点一次服务端。

## 已知限制

- Elicitation 只支持 `form` 模式，不支持 `url` 模式；Sampling 不支持工具调用（`tools` / `toolChoice`），图片、音频等非文本输入换成占位文字，只生成文本。
- 工具列表有缓存（`<数据目录>/mcp-tools.json`）：服务端返回的 `ttlMs` 决定有效期，没给时按 10 分钟；`ttlMs: 0` 表示不缓存。指纹按连接目标计算，改地址或命令后旧缓存自动失效。`/mcp` 面板的 `r` 会先清缓存再重连。`ttlMs` 与 `cacheScope` 里只用了 `ttlMs`，`cacheScope`（`public` / `private`）忽略——缓存只在本机、单用户。
- OAuth 只做公开客户端（PKCE，无 client secret），不支持 Client ID Metadata Document；登录只能在终端用 `sprout mcp login`，TUI 与 `sprout serve` 里不触发浏览器授权。令牌文件明文存储（靠文件权限保护），没有接系统钥匙串。
- 开关只有 TUI 的 `/mcp` 面板；没有 WebUI 管理面板（WebUI 是 M4）。面板不做配置编辑（加服务端、改地址），这些仍要手改 `mcp.jsonc`。
- 没有 `resources/subscribe`；`notifications/tools/list_changed` 已处理：收到后客户端在后台重新 `tools/list`，TUI 在空闲时重建 agent，工具变化不需要重启。
- 通知只在已有请求的 SSE 流里接收（Streamable HTTP 没有单独开长连接），stdio 服务端则直接读 stdin/stdout。HTTP 服务端如果只在独立的 GET 流里发通知，改动要等到下一次工具调用后才生效。

## 代码位置

- `internal/agent/mcp/mcp.go`：客户端、握手与版本协商（`KnownVersion` 缓存命中、失败回退）、工具列表、调用
- `internal/agent/mcp/versions.go`：协议版本缓存（读写、目标指纹失效）
- `internal/agent/mcp/toolcache.go`：工具列表缓存（`ttlMs` 有效期、目标指纹失效）与 `list_changed` 后的刷新
- `internal/agent/mcp/stdio.go`、`http.go`：两种传输
- `internal/agent/mcp/jsonrpc.go`：JSON-RPC 消息结构
- `internal/agent/mcp/resource.go`：资源列表与读取（`resources/list`、`resources/templates/list`、`resources/read`）
- `internal/agent/mcp/resourcetool.go`：把资源适配成两个 `tool.Tool`
- `internal/agent/mcp/prompt.go`：提示词列出与读取，以及 `list_prompts` / `get_prompt` 两个工具
- `internal/agent/mcp/input.go`：elicitation / sampling 请求的处理、MRTR 重试循环（`Client.rpc`）、等待输入时暂停超时
- `internal/agent/mcp/oauth_*.go`：OAuth 登录（`oauth_login.go`）、元数据发现与动态注册（`oauth_discover.go`）、令牌刷新（`oauth_token.go`）、令牌文件（`oauth_store.go`）
- `internal/agent/config/mcp.go`、`mcp_template.go`：`mcp.jsonc` 的解析、校验与模板；`mcp_edit.go` 是保留注释的单键写回（`SetMCPServerEnabled` / `SetMCPToolExcluded`）；`paths.go` 的 `MCPVersionsPath` 是缓存文件位置
- `cmd/sprout/mcp.go`：后台连接（`startMCP` / `waitMCP` / `applyMCP`）、环境变量展开、工具注册，以及 `/mcp` 面板的实现（`mcpStore`）；`mcp_cache.go` 接工具列表缓存与变更回调
- `cmd/sprout/mcp_input.go`：把输入请求转成 TUI 表单或 RPC 请求（`mcp_rpc.go`），采样用当前模型生成；`mcp_login.go` 是 `sprout mcp login/logout`
- `internal/tui/ask.go`：表单与采样确认框
- `internal/agent/platform/open_url.go`：用系统浏览器打开授权地址
- `internal/tui/mcp.go`：`/mcp` 面板的交互与渲染；`model.go` 的 `Options.MCP` / `Options.ApplyMCP` 与“连接中”状态，`update.go` 在空闲时接入

## 测试

- `internal/agent/mcp/mcp_test.go`：旧版 HTTP 握手与调用、无状态 `server/discover`、SSE 响应、HTTP 错误、工具名清洗。
- `internal/agent/mcp/resource_test.go`：资源能力检测、分页列表、`-32601` 当作空、文本与图片资源的读取、非图片二进制只写说明。
- `internal/agent/mcp/versions_test.go`：版本缓存往返、目标变化失效、令牌不落盘、坏文件容错、按缓存版本跳过探测、过期缓存回退。
- `internal/agent/mcp/stdio_test.go`：真实子进程充当服务端（测试二进制自举），验证握手、分页外的调用、`isError`、命令不存在、超时。
- `internal/agent/mcp/prompt_test.go`：提示词能力检测、分页、参数传递、消息渲染。
- `internal/agent/mcp/input_test.go`、`legacy_input_test.go`：能力只在接了处理函数时声明、表单与采样的请求解析和回复、旧版协议下 stdio 与 HTTP SSE 里的服务端请求、等待输入时不超时。
- `internal/agent/mcp/mrtr_test.go`、`mrtr2_test.go`：`input_required` 补充输入后重发、`requestState` 原样回传、多轮、轮数上限、输入失败。
- `internal/agent/mcp/oauth_test.go`（替身服务在 `oauth_fake_test.go`）：完整登录流程、授权参数、复用客户端 ID、拒绝不安全的服务端（无 S256、issuer / resource 不一致、无法注册）、回调校验（iss、state、Host、报错）、取消、刷新与并发、`invalid_grant`、URL 变化、令牌文件损坏、401 / 403 提示、`headers` 优先。
- `internal/agent/config/mcp_test.go`：模板合法、示例与模板一致、缺文件不报错、校验一次报全、按 enable 过滤。
- `internal/agent/config/mcp_edit_test.go`：写回保留注释/BOM/权限、非法改动回滚、工具开关与 `excludeTools` 的双向切换、单行与空对象的插入排版。
- `internal/tui/mcp_test.go`：面板列出服务端与工具、开关服务端与工具、展开/收起、刷新、失败提示、忙时忽略按键、生成中拒绝打开。
- `cmd/sprout/mcp_input_test.go`、`mcp_rpc_test.go`：表单字段转换与顺序、采样确认与拒绝、RPC 转发的能力检查与回答校验、客户端断开。
- `cmd/sprout/mcp_login_test.go`：`sprout mcp login/logout` 的参数解析、真实配置与令牌文件上的登录退出、连接时接上令牌来源、`oauth` 配置校验。
- `cmd/sprout/mcp_test.go`：`${VAR}` 展开、`excludeTools` 生效、注册名前缀、连接失败只记录不致命、后台连接后工具接入且保留历史、面板开关写回配置并重建注册表、刷新重读配置。
