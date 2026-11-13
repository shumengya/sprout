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
  "exec": { ... },         // 外部工具的执行方式；内置工具写了会报错
}
```

- 工具名：字母开头，后接字母、数字、`_`、`-`，最长 64 字符。模型把它当函数名用。
- 没写出的工具视为**未注册**；`enable: false` 或省略 `enable` 同样不启用。
- 内置工具（`bash`、`powershell`、`read`、`write`、`edit`）由程序实现，`description` / `parameters`
  只覆盖默认值，省略即用内置值。`parameters` 同样要满足 `type: "object"`，写错启动即报错。
  `bash` / `powershell` 覆盖描述时，程序仍会在末尾追加
  当前环境的版本说明（如 PowerShell 5.1 不支持 `&&`），避免写死的描述误导模型。
- 其他名字是外部工具：必须给 `description` 与 `exec`，否则启动报错。
- 写错名字或字段在启动时直接报错并列出可用工具，不会悄悄失效。
- 修改后需要重启 sprout 才会生效；`/model` 切换模型时按同一份配置重建工具表。
- 模型的 `tools: false` 优先级更高：该模型下不向服务端提供任何工具。

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

模板与仓库根目录的 `tools.example.jsonc` 由测试保证一致。

## 与 MCP 工具的关系

MCP 服务端提供的工具走同一张注册表，名字统一是 `mcp__<服务端>__<工具>`（见 `docs/mcp.md`）。它们排在 `tools.jsonc` 的内置与外部工具之后注册：

- `tools.jsonc` 管不到 MCP 工具：开关与屏蔽在 `mcp.jsonc` 的 `enable` / `excludeTools` 里。
- 重名时先注册的赢（内置 > 外部 > MCP），被跳过的写在 `/tools` 的不可用列表里。
- 模型 `tools=false` 时不连接任何 MCP 服务端。
