# Agent Skills

Sprout 从 M1 之后支持 Agent Skills：一份目录 + 一个 `SKILL.md`，启动时只把 `name` 与 `description` 放进系统提示词，正文在任务命中时才读进来。规范见 [agentskills.io/specification](https://agentskills.io/specification)。

## 为什么接它

技能解决的是“领域知识按需加载”：常驻上下文里只放一个目录（每个技能约 50–100 token），模型判断某个技能相关时再读正文。装二十个技能不等于二十份说明压满上下文。

它和相邻能力的分工：

| 能力 | 形态 | 加载时机 |
| --- | --- | --- |
| `AGENTS.md` | 单个 Markdown | 每次请求都在系统提示词里 |
| 技能 | 目录 + `SKILL.md` | 元数据常驻，正文按需 `read` |
| MCP | JSON-RPC 服务端 | 工具调用时（见 `docs/mcp.md`） |

## 注册表：skills.jsonc

**技能必须注册到 `<数据目录>/skills.jsonc` 才会生效**，和 `mcp.jsonc` 一样。扫描目录只是“发现”技能，发现后要登记进注册表，否则不会交给模型。

```jsonc
{
  // 技能注册表：只有注册在这里的技能才会交给模型。
  "skills": {
    // 手动注册示例（去掉注释即可，path 指向含 SKILL.md 的目录，相对路径按数据目录解析）：
    // "pdf": {"path": "D:/work/my-skills/pdf", "enable": true}
  },

  // 是否自动加载 .agents/skills（项目里与用户主目录下的）并登记到上面的注册表。
  // 数据目录 skills/ 下的技能总是自动登记，不受这一项影响。
  "scanAgents": true
}
```

| 字段 | 含义 |
| --- | --- |
| `skills.<名字>.path` | 技能目录（含 `SKILL.md` 的那一层）。可以是任意位置，不必在扫描目录里；相对路径按数据目录解析。 |
| `skills.<名字>.enable` | 是否把技能交给模型。`false` 时注册信息保留，`/skills` 面板里还能重新打开。 |
| `scanAgents` | 是否加载 `.agents/skills` 下的技能，缺省为 `true`。设为 `false` 后这些目录下的技能一律不加载，注册表里的条目保留。 |

- 注册表的键就是交给模型的名字。和 `SKILL.md` 里的 `name` 不一致时以注册名为准，并给一条提示。
- `path` 指向的目录不存在或 `SKILL.md` 解析失败时，只跳过这一个技能并提示，不影响其他技能。
- 文件写坏了（JSONC 语法错误）会在启动时报错，而不是悄悄当成空表——注册表是唯一入口，静默清空会让所有技能一起消失。
- 旧版只有 `"disabled": [...]` 的配置在启动时自动迁移成注册表格式，禁用状态保留。

## 扫描位置与自动注册

按优先级从高到低（靠前的先被采用）：

| 顺序 | 路径 | 范围 | 自动注册 |
| --- | --- | --- | --- |
| 1 | `<工作目录>/.sprout/skills/` | 项目 | 是 |
| 2 | `<数据目录>/skills/` | 用户（安装级） | 是 |
| 3 | `<工作目录>/.agents/skills/` | 项目（跨客户端约定） | 是，受 `scanAgents` 控制 |
| 4 | `~/.agents/skills/` | 用户（跨客户端约定） | 是，受 `scanAgents` 控制 |

- 数据目录默认是 `<exeDir>/data/.sprout`；启动时会确保 `<数据目录>/skills/` 存在，方便用户知道往哪放。
- 扫描到的新技能会被**自动登记**进 `skills.jsonc`（`path` + `enable: true`），下次扫描不会重复写入。已有的条目不会被覆盖，用户改过的 `path` 和 `enable` 一直有效。
- 想放在别处、又不想挪进扫描目录的技能：直接在 `skills.jsonc` 里手动写一条 `path`，它会以“自定义”来源加载。
- `.agents/skills` 是别的 agent 客户端的目录，可以在 `/skills` 面板里按 `a` 或改 `scanAgents` 整体关掉；关掉后那里的技能不加载，注册表条目保留，改回 `true` 即恢复。被这个开关挡住的技能**不逐条提示**——那个目录里往往一次就有十来个，逐个提示会刷满对话区；状态在 `/skills` 面板里看得到。
- 只认“子目录里存在 `SKILL.md`”的目录；同级散落的 `README.md` 不算技能。
- 扫描跳过 `.git/`、`node_modules/`、以 `.` 开头的目录；最深 4 层、最多 2000 个目录。
- 找到含 `SKILL.md` 的目录后不再向下深入：技能自带的 `scripts/`、`references/` 不会被当成另一个技能。
- 工作目录不是绝对路径时不扫项目级（`go run` 与测试里可能出现）。

同名技能先到先得；**内容不同**时记一条“被遮蔽”提示，**内容完全相同**（例如 `.agents/skills` 与 `~/.claude/skills` 互为副本）时静默跳过。

## `SKILL.md` 格式

```markdown
---
name: pdf-processing
description: 提取 PDF 文本、填写表单、合并文件。处理 PDF 或用户提到表单时使用。
license: Apache-2.0
compatibility: Requires Python 3.14+ and uv
metadata:
  author: example-org
allowed-tools: Bash(git:*) Read
---

# PDF Processing

步骤、示例、边界情况……
```

用到的字段：

| 字段 | 必填 | 处理方式 |
| --- | --- | --- |
| `name` | 是 | 缺省时用目录名并提示；与目录名不一致、或不符合 `^[a-z0-9]+(-[a-z0-9]+)*$` / 超过 64 字符时提示但照常加载 |
| `description` | 是 | **为空则跳过该技能**；超过 1024 字符截断并提示；折叠成一行放进目录 |
| `license` / `compatibility` / `metadata` / `allowed-tools` | 否 | 解析后保留在 `Skill` 结构里，目前不展示 |

未知字段忽略。YAML 不合法时（常见原因是未加引号的冒号）退一步逐行取 `name` / `description`，取不到 `description` 才跳过，并提示“已按宽松模式读取”。

## 渐进式披露在 sprout 里的落点

1. **目录**：启动时 `skills.Discover` 扫一遍，`skills.AppendCatalog` 把 `<available_skills>` 段追加在系统提示词末尾（含 `name`、`description`、`location`）。和 `AGENTS.md` 一样追加而不是做成模板变量：用户的 `SYSTEMPROMPT.md` 可能是旧版本或完全自写，做成变量会让技能静默失效。
2. **正文**：模型用内置 `read` 工具读 `location`（即 `SKILL.md` 的绝对路径）。没有注册 `read` 时，提示词改成让它用 `bash` / `powershell` 打开。
3. **资源**：正文里引用的 `scripts/…`、`references/…` 由模型按需再读；目录段会提醒把相对路径拼成绝对路径。

不注册额外的“激活技能”工具：`read` 已经能做这件事，多一个工具只是多一份 schema 常驻上下文。

## /skills 面板

TUI 里输入 `/skills` 打开技能面板，替换输入框：

```
技能                                                   已启用 2/3
↑↓ 选择 · 空格 / Enter 开关 · a .agents 自动加载：开（按 a 切换）· r 重新扫描 · Esc 关闭
● lark  项目  飞书文档读写
● paper 用户  论文检索与摘要
○ pdf   用户  PDF 提取文本
(2/3)
```

| 按键 | 作用 |
| --- | --- |
| `↑` `↓` / `k` `j` | 移动光标 |
| `PgUp` `PgDn` / `Home` `End` | 翻页、跳到首尾 |
| `空格` / `Enter` | 启用 / 停用当前技能（写回该条的 `enable`） |
| `a` | 打开 / 关闭 `.agents/skills` 的自动加载（写回 `scanAgents`），立即重新扫描 |
| `r` | 重新扫描技能目录：新技能自动注册，已有开关保留 |
| `Esc` / `Ctrl+C` | 关闭面板 |

- 面板列出的是**注册表里的技能**。改开关只改 `skills.jsonc` 里对应条目的 `enable`，注释与其余条目原样保留。
- **停用只影响“是否把技能的 name / description / location 告诉模型”**：`SKILL.md` 和技能目录下的文件都不动，重新启用后下一轮对话即可用。
- 改完会重建 agent（技能目录在系统提示词里），对话历史与摘要保留；面板操作期间不接受新的开关，避免并发改同一份状态。
- 生成、压缩、恢复会话期间不能打开面板。
- `sprout run` 同样遵守 `skills.jsonc`，脚本里的单次提问也不会看到被停用的技能。

## 手动触发

除了让模型按 description 自己判断，也可以直接点名：在 TUI 里输入 `/技能名 任务`，例如 `/lark 整理这份会议记录`。输入 `/` 时候选列表会在内置命令之后列出已启用的技能。

- 发给模型的是一条组装好的提问：技能名、技能目录（正文里的相对路径以它为准）、`SKILL.md` 去掉 frontmatter 的正文，以及任务。正文直接放进消息，模型不用再调 `read`。不写任务时让模型按技能说明开始，缺信息先问。
- 对话区和会话记录显示的是你输入的原文 `/lark ...`，下方提示“已手动触发技能 lark”。
- 名字不区分大小写；对不上任何技能时整段按普通提问发送，所以 `/etc/hosts 是什么` 这类输入不受影响。内置命令（`/mcp`、`/skills` 等）优先，技能与内置命令重名时只能通过模型自动触发。
- 停用的技能不能手动触发，会提示先在 `/skills` 里打开。
- 手动触发每次都读当前磁盘上的 `SKILL.md`，改完正文不用重新扫描。

## 查看已加载的技能

TUI 里 `/tools` 的最后一段列出技能名，停用的带“（已停用）”；状态栏第三行的“技能”平时显示总数，有技能被停用时显示 `启用/全部`，没装技能时显示 `—`。启动日志里有每个技能的名字、来源和路径。

**技能被触发时对话区会标出来**：模型读到某个技能的 `SKILL.md`（或它目录下的 `references/`、`scripts/`）时，那张工具卡片最前面显示绿色的 `[技能] 名字`，一眼能看出这次是在按技能办事，而不是普通读文件。识别只看参数里的路径是否落在已安装技能的目录里，`read`、`bash`、`powershell` 都覆盖；路径比较忽略大小写，`\` 与 `/` 等价（Windows 上同一个文件常有两种写法）。标记纯属展示，不影响工具执行。

技能解析失败、被遮蔽、描述超长这类问题会以提示出现在 TUI 的对话区顶部（`sprout run` 时写到 stderr），不会阻止启动。

## 安全说明

项目级技能来自当前仓库，克隆来的陌生仓库可能带着技能文件——技能正文是给模型的指令，等价于把仓库里的文字塞进上下文。目前没有信任门槛和权限模块，这是已知取舍（见 `TODOS.md` 的“已知限制与风险”）。只想给当前工具用时，把技能放进数据目录，不要放进项目目录。

## 代码位置

- `internal/agent/skills/`：发现、frontmatter 解析、目录渲染（`skills.go`）
- `internal/agent/config/skills.go`：`skills.jsonc` 注册表的读写（复用 `mcp_edit.go` 里的 JSONC 就地改写）
- `internal/tui/skills.go`：`/skills` 面板（`skillPicker`）与 `SkillPanel` 接口
- `cmd/sprout/skills.go`：扫描、自动注册、按注册表加载，以及面板实现（`skillStore`）
- `cmd/sprout/setup.go`：`buildAgent` 里调用 `skills.AppendCatalog`，只传启用的技能

## 测试

- `internal/agent/skills/skills_test.go`：发现规则、跳过目录、优先级与遮蔽、宽松解析、块标量、截断、目录渲染。
- `internal/tui/skill_test.go`：技能标记在 `read` 与 shell 命令下都出现、路径大小写与分隔符不影响识别、普通文件不误标、没装技能时不标。
- `internal/agent/config/skills_test.go`：模板可解析、`enable` / `scanAgents` 就地改写并保留注释、未注册技能报错、自动注册只写一次、缺 `skills` 对象时补建。
- `internal/tui/skills_test.go`：列表与状态、开关、重新扫描、`a` 切换 `.agents` 自动加载、失败提示、忙时忽略按键、生成中拒绝打开、空列表。
- `cmd/sprout/skills_test.go`：目录顺序与 `scanAgents` 开关；自动注册与重扫保留开关；手动注册任意路径；注册名优先于 frontmatter 名字；`.agents` 开关控制加载；旧 `disabled` 列表迁移；坏配置启动报错；停用后系统提示词里不再有该技能。
