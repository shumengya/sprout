# 工具注册（tools.jsonc）

工具是否交给模型、模型看到什么描述和参数，都由数据目录下的 `tools.jsonc` 决定。
文件不存在或为空时，启动会用内置模板重建。

设计参照 pi：一个工具就是一份普通定义（名字 + 描述 + JSON Schema 参数 + 执行方式）。
区别是把「执行方式」也做成了配置，所以 `ls`、`rg` 这类工具不必写 Go 代码。

## 条目字段

```jsonc
"<工具名>": {
  "enable": true,          // 必须为 true 才注册
  "description": "...",    // 给模型看：什么时候用、做什么、边界在哪
  "parameters": { ... },   // JSON Schema，type 必须是 object
  "parallel": false,       // 可选：同一批调用里能否和别的调用并行
  "background": true,      // 可选：能否转入后台执行
  "exec": { ... },         // 外部工具的执行方式；内置工具写了会报错
}
```

- 工具名：字母开头，后接字母、数字、`_`、`-`，最长 64 字符。模型把它当函数名用。
- 没写出的工具视为**未注册**；`enable: false` 或省略 `enable` 同样不启用。
- 内置工具（`bash`、`powershell`、`read`、`write`、`edit`、`delete`、`agent`）由程序实现，`description` / `parameters`
  只覆盖默认值，省略即用内置值。`parameters` 同样要满足 `type: "object"`，写错启动即报错。
  `agent` 派出子代理，子代理的类型不在这里配置，见 `docs/subagents.md`；覆盖它的 `parameters` 时要保留 `prompt`、`description`、`agent` 三个字段名。
  `bash` / `powershell` 覆盖描述时，程序仍会在末尾追加
  当前环境的版本说明（如 PowerShell 5.1 不支持 `&&`），避免写死的描述误导模型。
- 其他名字是外部工具：必须给 `description` 与 `exec`，否则启动报错。
- 写错名字或字段在启动时直接报错并列出可用工具，不会悄悄失效。
- 修改后需要重启 sprout 才会生效；`/model` 切换模型时按同一份配置重建工具表。
- 模型的 `tools: false` 优先级更高：该模型下不向服务端提供任何工具。

## 并行

模型在同一次回复里发出的多个工具调用默认并行执行，最多同时 4 个（`Config.MaxParallelTools`）。
工具可以声明自己不可并行（实现 `tool.Parallelizable`，`Parallel()` 返回 false），这种调用**独占执行**：

- 等同一批里排在它前面的调用全部结束才开始；
- 执行期间不启动别的调用；
- 结束后才轮到排在它后面的调用。

内置的 `write`、`edit`、`delete` 声明为不可并行，两个调用并发改同一个文件会互相覆盖。`bash`、`powershell`、
`read`、`agent`、外部工具和 MCP 工具默认可并行。`tools.jsonc` 里的 `parallel` 可以覆盖工具自己的声明，
比如给会改共享状态的外部工具写 `"parallel": false`。无论怎样调度，历史里的工具结果始终按调用顺序排列。

## 后台执行

跑得久的工具可以声明**可后台**（实现 `tool.Backgroundable`，`Background()` 返回 true）。内核会在这种工具的参数 schema 末尾加一个布尔参数 `run_in_background`，模型每次调用时自己决定要不要用。参数描述要求模型默认不用：前台执行更简单可靠，只有调用明显要跑很久、而等待期间确实有别的独立工作要做时才放到后台；只是想同时做几件事时不需要它，同一次回复里的多个调用本来就并行。

- 不写或为 false：和普通工具一样前台执行，等结果。
- 为 true：调用当场返回一段占位说明（含任务编号，就是这次 tool call 的 id），真正的执行在后台继续，ReAct 循环接着请求模型。后台调用不占这一批的并行名额，但在派发顺序里照样排在前面的独占调用之后。
- 执行结束后，结果作为一条以 `[后台任务已结束：<工具>，任务编号 <id>]` 开头的用户消息送进历史。送达只发生在安全点：一批工具执行完之后（排在用户的排队输入之前），或者模型给出没有工具调用的回答之后。
- 后台任务属于发起它的那一轮：模型给完答案时还有后台任务没结束，这一轮就等它们，结果到了再请求一次模型。等待期间用户排队的输入会马上接进来，不必等后台任务结束。
- 取消这一轮会取消全部后台任务；一轮出错或超过步数结束时，还在跑的后台任务被终止。它们的结果（“被取消”“被终止”加上已收集的输出）照样送进历史，模型下一轮能看到。

`run_in_background` 在执行前从参数里去掉，工具自己看不到它。不可并行的工具不能后台执行：后台调用一定会和之后的调用重叠，独占执行的承诺兑现不了。所以 `tool.Background` 同时要求可并行，`tools.jsonc` 里 `background: true` 与 `parallel: false` 同时写会报错，给默认不可并行的 `write` / `edit` / `delete` 写 `background: true` 也会在启动时报错。

内置工具里只有 `agent` 默认可后台。`tools.jsonc` 的 `background` 可以覆盖工具自己的声明：给跑得久的外部工具（测试、构建）写 `"background": true`，或者给 `agent` 写 `"background": false` 不让模型把子代理放到后台。后台结果不带图片：目前能转入后台的工具都不产出图片。

## exec

| 字段 | 说明 |
| --- | --- |
| `shell` | `bash` / `powershell` / `direct`。前两者经 shell 解释；`direct` 直接启动程序，不经 shell |
| `command` | `bash` / `powershell` 下是完整命令行；`direct` 下是可执行文件路径（也可写 `"git status --short"` 简写） |
| `args` | 仅 `direct` 使用，按顺序传给程序；每一项都可以含占位符 |
| `timeoutSeconds` | 省略取 120，最大 600 |

### 参数占位符

命令里的 `{{参数名}}` 会替换成模型传入的值：

- `bash` 下自动加单引号并转义，路径带空格、引号、`$()` 都不会被 shell 解释；
- `powershell` 下按单引号字符串转义（`'` 写成 `''`）；
- `direct` 下按原样作为独立 argv 项，不转义、不展开变量；
- 参数没传时替换成空串；`parameters.required` 里列了却没传会直接报错并点名。

数组与对象类型的值按 JSON 序列化后代入。

### 需要哪个 shell

- `exec.shell` 为 `bash` / `powershell` 时，本机必须探测到对应 shell。没有的话这个工具
  不注册，原因显示在 TUI 的 `/tools` 里，不影响其他工具。
- `direct` 不需要 shell。

## 内置工具

| 工具 | 参数 | 说明 |
| --- | --- | --- |
| `bash` | `command`、`timeout_seconds` | 执行 bash 命令；Windows 上需要真实的 Git Bash / MSYS2 / Cygwin |
| `powershell` | `command`、`timeout_seconds` | 执行 PowerShell 命令；优先 `pwsh` 7+，回退 5.1 |
| `read` | `path`、`offset`、`limit` | 按行读文本文件：默认从第 1 行读 2000 行，行号随内容返回 |
| `write` | `path`、`content` | 创建或覆盖文本文件，自动建父目录，先写临时文件再原子替换 |
| `edit` | `path`、`old_string`、`new_string`、`replace_all` | 把文件里的一段原文换成新内容：默认要求唯一匹配，匹配到多处或找不到都报错且不改文件；`replace_all` 替换全部。保留原换行符风格与权限 |
| `delete` | `path`、`recursive`、`dry_run` | 删除文件或目录，不进回收站。空目录可直接删，非空目录必须 `recursive: true`（否则报错并给出文件数、目录数与总大小）；`dry_run: true` 只统计不删。符号链接只删链接本身；Windows 只读文件先去掉只读位。磁盘根目录、工作目录及其上级、用户主目录及其上级一律拒绝 |

## 示例

包装 `ls`：

```jsonc
"ls": {
  "enable": true,
  "description": "列出目录内容，返回权限、大小和修改时间，不递归。",
  "parameters": {
    "type": "object",
    "properties": { "path": { "type": "string", "description": "要列出的目录" } },
    "required": ["path"]
  },
  "exec": { "shell": "bash", "command": "ls -la {{path}}" }
}
```

包装一个不需要 shell 的程序：

```jsonc
"git-status": {
  "enable": true,
  "description": "显示 git 工作区状态。",
  "exec": { "shell": "direct", "command": "git", "args": ["status", "--short"] }
}
```

覆盖内置工具的描述与参数（不需要 `exec`）：

```jsonc
"write": {
  "enable": true,
  "description": "只允许写工作目录下的 .md 文件。",
  "parameters": {
    "type": "object",
    "properties": { "path": { "type": "string" }, "content": { "type": "string" } },
    "required": ["path", "content"]
  }
}
```

注意：覆盖 `parameters` 只改变模型看到的 schema，**不会**让工具本身拒绝其他参数——
约束执行行为仍要改代码。这是当前实现的边界，不是沙箱。

模板与 `examples/tools.example.jsonc` 由测试保证一致。

## 与 MCP 工具的关系

MCP 服务端提供的工具走同一张注册表，名字统一是 `mcp__<服务端>__<工具>`（见 `docs/mcp.md`）。它们排在 `tools.jsonc` 的内置与外部工具之后注册：

- `tools.jsonc` 管不到 MCP 工具：开关与屏蔽在 `mcp.jsonc` 的 `enable` / `excludeTools` 里。
- 重名时先注册的赢（内置 > 外部 > MCP），被跳过的写在 `/tools` 的不可用列表里。
- 模型 `tools=false` 时不连接任何 MCP 服务端。
