# TODOS

M1（最小可用闭环）已实现并提交。本文件区分三类状态：**已实现**、**已实现但未验证**、**未实现**。
验收清单的逐项状态见 `docs/m1-implementation.md` §12，与原计划的差异见 §15。

## 一、已实现

### 内核（`internal/agent`）
- [x] ReAct 循环：模型调用 → 工具执行 → 结果回传，轮数默认不设上限（`Config.MaxSteps > 0` 时限制，超过以 `max_steps` 结束）；停止靠用户取消或模型自己收尾。
- [x] 事件协议（`event`）：`turn.started` / `message.delta` / `reasoning.delta` / `message.completed` / `tool.requested` / `tool.finished` / `usage` / `retrying` / `compacted` / `interrupted` / `turn.finished` / `error`；每轮必以 `turn.finished` 收尾。
- [x] 取消：`context` 贯穿 HTTP 请求与工具进程；取消时为未执行的 tool call 补“未执行”结果，保证历史合法。
- [x] 中断保留：被取消或中途失败的轮次不丢弃——已收到的正文与思维链连同中断说明（`[对话中断：用户取消]` 或错误原文）作为一条 assistant 消息留在历史里，并发出 `interrupted` 事件；会话按 `interrupted` 记录落盘，恢复后模型看到的和当初一致。半截 tool call 丢弃（参数可能不是合法 JSON）。
- [x] 失败回滚：panic 被 recover 并还原历史；旧会话重放沿用当年的撤销规则。
- [x] 空工具参数规范化为 `{}`；工具的任何失败都转成 tool 消息回给模型，不终止 agent。
- [x] 系统提示词只描述实际可用的工具；两个 shell 都可用时说明选择规则。
- [x] 系统提示词外置：数据目录 `SYSTEMPROMPT.md` 是 `text/template` 模板，缺失或为空时写入内置模板，写错时启动报错（`docs/system-prompt.md`）。
- [x] `AGENTS.md`：数据目录一份全局说明，工作目录到根各级的项目说明按从宽到窄追加在系统提示词末尾（带路径标签，单份 32 KB 上限，读不了只提示）；见 `docs/system-prompt.md`。
- [x] 思维链：保存 `reasoning_content` 并在后续请求中回传。
- [x] 并行工具：同一批 tool call 并发执行（`MaxParallelTools` 默认 4，1 为串行）；事件与历史写入留在主 goroutine，历史里的 tool 消息始终按调用顺序；工具 panic 只算该次调用失败。真实 DeepSeek 验证过两个各睡 4 秒的命令重叠执行。
- [x] 自动重试：网络失败、408/409/425/429/5xx、流被截断按指数退避重试（默认 7 次，等待 1s、2s、4s…64s，不加抖动），听 `Retry-After`（超过 64 秒不等）；`insufficient_quota` 不重试；本次调用已向界面输出内容后不再重试，避免重复输出；退避期间可取消。
- [x] 上下文管理：`contextWindow` 接近预算时先从最旧的开始裁剪旧工具输出（按 token 估算降到预算的 85% 以下就停，不一次裁光），不够再把更早的对话总结成分层摘要（每次只总结新切掉的一段并追加成新的一层，超过 3 层合并最旧两层；只在用户消息处切，不拆 tool call 与结果）；服务端报告超长时压缩一次再重试；摘要失败不丢历史；每次压缩都有事件和提示。真实 DeepSeek 验证过（调小窗口后连续压缩 4 次、暗号未丢；该验证早于“按需裁剪”和“分层摘要”两处改动，之后只有单元测试与黄金重放测试）。
- [x] 会话恢复：`-c` / `--resume`（`run` 与 TUI 都支持）和 TUI 的 `/resume`；重放规则与运行时一致（并行工具结果按调用顺序、被杀留下的未完成调用补“未执行”、首次调用失败撤销用户消息、压缩按当初方式重做），换模型时丢弃旧思维链；恢复后接着写原会话文件。真实 DeepSeek 验证过 `run -c` 能记住上文。
- [x] 会话浏览器：TUI 的 `/resume` 替换输入框列出会话，可切当前目录 / 全部、搜索（关键词 / `re:` 正则 / `"短语"`）、按最近 / 最早 / 轮数排序、只看已命名、重命名（会话文件里追加 `name` 记录）、删除（连同图片，需确认，当前会话不能删）。

### Provider（`internal/provider/openai`）
- [x] OpenAI Chat Completions 流式：按 index 拼 tool call（兼容同 index 不同 id 的服务）、`include_usage`、`finish_reason`。
- [x] OpenAI Responses 流式：`store:false` + 完整 input，扁平工具声明且 `strict:false`，多来源拼装 `function_call`，文本不重复。
- [x] 错误归一化为 `ProviderError`（带 `Retryable`），中文提示；流被截断视为可重试错误；API Key 不进入任何错误信息。

### Provider（`internal/provider/anthropic`、`internal/provider/google`）
- [x] Anthropic Messages 流式：text / image block，思考块按声明发 `thinking`，思考块带 `signature` 原样回传（`docs/effort.md`）。
- [x] Google Gemini 流式：`streamGenerateContent`，part 数组，`inlineData` 传图，`thinkingConfig` 调思考程度。
- [x] 四协议共用的 HTTP / SSE 传输层抽到 `internal/provider/transport`。

### 工具（`internal/agent/tool`）
- [x] `bash`（Git Bash / MSYS2 / Cygwin / 系统 bash）与 `powershell`（pwsh 7+ 优先，回退 5.1）。
- [x] 工具注册表 `tools.jsonc`：每个工具写完整定义——`enable`、`description`、`parameters`（JSON Schema）、`exec`（外部命令封装）；只有 `enable: true` 才注册。非内置名字没有 `exec`、内置工具写了 `exec`、名字不合法、参数 schema 不是对象等都在启动时报错。设计见 `docs/tools.md`。
- [x] 外部工具：`exec.shell` 支持 `bash` / `powershell` / `direct`，命令里的 `{{参数名}}` 按 shell 规则加引号转义（direct 下按 argv 原样传），必填参数缺失直接报错；需要但本机没有的 shell 不注册并在 `/tools` 说明。模板内置 `ls` 示例。
- [x] 内置工具的描述与参数可由 `tools.jsonc` 覆盖（`tool.Decorator`）；`bash` / `powershell` 覆盖描述时仍追加当前环境与版本说明。
- [x] `read`：按行号读文本文件，可指定 `offset` / `limit`，拒绝二进制与超大文件，输出超 50KB 自动截断并提示续读位置。
- [x] `write`：创建或覆盖文本文件，自动建父目录，先写同目录临时文件再原子替换；覆盖时沿用原权限。
- [x] `delete`：删除文件或目录，不进回收站。空目录可直接删，非空目录必须显式 `recursive: true`，否则报错并列出文件数、目录数与总大小；`dry_run: true` 只统计不删，方便删前确认影响范围。符号链接只删链接本身、不碰目标；Windows 只读文件先去掉只读位再删；扫描阶段出错时一项不删，删除阶段逐个失败并汇总报错。磁盘根目录、工作目录及其上级、用户主目录及其上级一律拒绝。
- [x] `edit`：按 `old_string` → `new_string` 替换文件里的一段内容，省掉整份重写。默认要求唯一匹配，0 处或多处都报错并列出所在行号且不改文件，`replace_all` 才替换全部；匹配前把 CRLF 归一成 LF、写回时恢复，读出来抄的片段不会因为行尾对不上而失配；拒绝二进制、目录和超大文件，写回同样走临时文件原子替换并沿用原权限。
- [x] 平台探测：按文档顺序查找；`System32\bash.exe`（WSL 启动器）判为不可用并提示安装 Git for Windows。开关已开但本机没有 shell 时，该工具不注册并在 `/tools` 里说明原因。
- [x] 执行契约：每次新进程、工作目录固定、默认超时 120s（上限 600s，模型可传 `timeout_seconds`）、输出上限 50KB（头尾保留）、stdin 置空。
- [x] 超时/取消终止整个进程树（Unix 进程组，Windows `taskkill /T`），`WaitDelay` 防止孙进程占管道。
- [x] PowerShell 注入 UTF-8 输出编码前导；工具描述按 edition 区分语法。

### 配置与数据目录（`internal/agent/config`）
- [x] `models.jsonc`（JSONC，注释与尾逗号）；语法/类型错误定位到 `文件:行:列`（列按字符计）。
- [x] 校验一次汇总所有问题；API Key 环境变量优先、`apiKey` 兜底；`tools:false` 的模型不下发工具。
- [x] 数据目录 `--data-dir` > `SPROUT_DATA_DIR` > `<exeDir>/data/.sprout`；`go run` 临时二进制或不可写时报错，不回退 `~/.sprout`。
- [x] 首次运行生成带注释的模板（不覆盖已有文件）；模板与 `examples/models.example.jsonc` 由测试保证一致。

### 会话（`internal/agent/session`）
- [x] JSONL 记录器（`event.Sink`）：`session` / `user` / `assistant` / `tool` / `usage` / `turn` / `error`；文件懒创建，权限 0600。
- [x] 路径 `sessions/--<cwd slug>--/<时间戳>_<id>.jsonl`；元数据都在会话文件里（`session` 头、`name` 记录），不再写分组索引 `meta.json`，旧文件只读来显示旧版本起的名字。

### TUI（`internal/tui`）
- [x] 全屏 Bubble Tea v2；`Update` 不做 IO，agent 在 `tea.Cmd` 的独立 goroutine 里运行。
- [x] 流式输出、工具卡片（`工具名(参数)` 函数调用形式、退出码、耗时、折叠时最多 8 行预览，点击展开完整参数与输出）、输出里的 ANSI/控制字符清理。
- [x] 历史滚动：鼠标滚轮、PgUp/PgDn、Ctrl+↑↓、Ctrl+Home/End；输入框为空时 ↑↓ Home End 同样滚动；状态行显示位置与“有新内容”。
- [x] Markdown（CommonMark + GFM）：标题、嵌套列表与任务列表、多级引用、分隔线、围栏代码块（chroma 语法高亮）、粗体/斜体/删除线/行内代码、链接与图片、表格（按显示宽度对齐、列对齐标记、过宽时缩列折行、极窄时逐行展开）。流式输出中途未闭合的语法原样显示。
- [x] 思维链淡色显示：流式预览最后 3 行；结束后折叠成一行“已思考 N秒 N token：内容…”，点击单条展开或收起，`/think` 一次切换全部。思维链 token 取服务端报告值（未报告时显示内核估算并标“约”）。token 数缩写为 2.9k、12k 这样。
- [x] `/help` `/tools` `/think` `/mouse` `/model` `/effort` `/new` `/compact` `/image` `/resume` `/install-sprout` `/uninstall-sprout` `/mcp` `/skills` `/quit`；缺 API Key 时禁止发送并显示配置提示。
- [x] `/mcp` 面板：替换输入框，列出全部 MCP 服务端与展开后的工具，空格开关、→ / ← 展开收起、`r` 重读配置并重连；开关直接写回 `mcp.jsonc`，保留注释与排版（`internal/tui/mcp.go`、`internal/agent/config/mcp_edit.go`）。
- [x] `/skills` 面板：列出注册的技能与来源，空格 / Enter 开关单个技能，`a` 切换 `.agents` 自动加载，`r` 重新扫描并自动注册新技能；开关写回 `skills.jsonc`，保留注释（`internal/tui/skills.go`）。
- [x] 技能手动触发：`/技能名 任务` 把技能正文直接交给模型；内置命令优先，名字不是技能时整段按普通提问发送；输入 `/` 的候选列表包含可触发的技能。设计见 `docs/skills.md`。
- [x] `/compact [说明]` 手动压缩：把前面的全部对话总结成摘要（分层摘要，说明可以让摘要特别保留某些内容；`/compact -k` 保留最近一轮原文），压缩期间禁止发送与切换，Esc 取消；会话落盘后恢复得到同样的结果。设计见 `docs/m1-implementation.md` §15。
- [x] 斜杠命令补全：输入 `/` 列出候选，Tab 填入、↑↓/Ctrl+P/N 选择、Esc 关闭；`/effort` 与 `/model` 的参数也参与补全。命令与快捷键说明见 `docs/slash-commands.md`、`docs/keybindings.md`。
- [x] `/model` 模型选择器：↑↓ 或直接输入过滤，Enter 切换；保留对话历史、清空旧模型思维链；生成中或恢复/切换中拒绝。
- [x] 记住上次的模型与思考档位：`/model`、`/effort` 切换成功后写进数据目录的 `settings.jsonc`，下次启动沿用；优先级 `--effort` > 上次档位 > `models.jsonc` 默认值；记下的模型已不在配置里或记录损坏时退回默认并提示（`internal/agent/config/state.go`，见 `docs/effort.md`）。
- [x] 生成中排队发送：生成中按 Enter 把消息排进队列；当前这批工具执行完后由内核取走、接在工具结果后面交给模型（`Input.Queued`、`user.queued` 事件、`user_queued` 记录），仍算同一轮；模型已在给最终答案时，本轮结束（正常、出错、取消）后按顺序作为新的一轮发出；排队的消息在对话区标暗色，不计入对话轮次；`/new` 清空队列。报错为亮红 ✗，重试提示为黄色 ⟳。
- [x] `/new` 清空对话区与上下文开始新会话，旧会话仍可用 `/resume` 找回。
- [x] `/image 路径` 附加图片，粘贴或消息里直接写图片路径时自动附加（`internal/media`、`internal/tui/image.go`）。
- [x] 对话区拖选复制：按住左键拖动选中（反色显示），松开自动复制；选区跟着内容走，拖到上下边缘自动滚动；中文按整个字选中；内容重排（窗口缩放、`/think` 展开）导致选区对不上时自动取消。复制同时走 OSC 52 和系统剪贴板，任何一条失败只提示、不当作致命。
- [x] 真实终端光标（输入法候选窗跟随）；退出时 `Shutdown` 等 agent 收尾，保证 `turn.finished` 被记录。
- [x] 底部状态栏（默认三行：路径、对话轮次、耗时、内存 百分比 · 进程 MB、CPU · 负载 / 输入、输出、缓存读写 · 命中率、花费、上下文占用、模型、思考强度 / 状态、速度、压缩次数、MCP、插件、技能、工具数、Git 分支 ↑ahead +新增 ~修改 -删除）；MCP 显示 `已连接/已启用`、技能显示个数，没配置时显示 `—`；`插件` 仍未实现，恒为占位。花费按 models.dev 的单价折算，见下。显示哪些项、在第几行、什么颜色由数据目录的 `statusline.jsonc` 配置（`examples/statusline.example.jsonc` 是模板）。设计见 `docs/m1-implementation.md` §9。

### 识图（`internal/media`，设计见 `docs/m2-vision.md`）
- [x] `Message.Content` 改为 text / image 块数组；工具结果也能带图片。
- [x] 四种协议都能发图；协议不支持在工具结果里放图时，在其后补一条 user 消息。
- [x] 会话把图片存成 `<会话名>.images/` 下按内容哈希命名的文件，JSONL 里只存相对路径；恢复时读回，文件丢失换成占位文本。
- [x] `models.jsonc` 的 `vision` 标记；未标注的模型发送时图片换成占位文本，历史不改。
- [x] 图片 token 估算（按尺寸折算，夹在 300–8000）；按内容嗅探类型；超过 3 MB 或长边超过 2000 像素时自动缩放、重新编码（应用 jpeg 的 EXIF 方向），见 `docs/m2-vision.md` §6.1。

### CLI 与入口（`internal/cli`、`cmd/sprout`）
- [x] `sprout`（TUI）、`sprout run "..."`（答案写 stdout，进度/错误写 stderr；退出码 0 / 1 / 2 / 130）。
- [x] `--effort`、`--image`（可重复）、`-c` / `--resume`；完整说明见 `docs/cli.md`。
- [x] `sprout update [--repo OWNER/NAME]`：查询 GitHub Releases 最新发布并与当前版本比较，不一致时打印发布页与本平台压缩包下载地址；有新版本时默认下载本平台产物、校验 `SHA256SUMS.txt`、解压、用 `--version` 试运行后替换当前程序（Windows 先把旧程序改名为 `.old`，下次启动清理）；`--check` 只报告不下载。没有校验文件、校验不符、试运行失败、程序目录不可写时都不动原程序。开发构建（`dev`）只报告最新发布；本地比最新发布还新时不劝降级；网络失败、限流、仓库无发布以退出码 1 结束（`cmd/sprout/update.go`、`selfupdate.go`）。TUI 启动时后台查一次（仅发布构建，失败静默），有新版本在对话区提示；`/update` 在 TUI 里执行同样的更新（`cmd/sprout/updater.go`、`internal/tui/selfupdate.go`）。
- [x] 模型输出与命令内容写终端前去除控制字符（含 ESC）。
- [x] Windows：控制台 UTF-8 并在退出时恢复；双击启动出错时等待回车；日志写 `logs/sprout-YYYYMMDD.log`（不记录 Key、请求体、用户输入）。

### 工程
- [x] `go build` / `go vet` / `go test` 通过；测试替身只出现在 `_test.go`。
- [x] 交叉编译（`CGO_ENABLED=0`）：linux/amd64、darwin/arm64、android/arm64 的 build + vet 通过（仅编译，未运行）。

## 二、已实现，但尚未验证

这些功能代码和测试都在，但没有在真实环境里确认过，使用时请留意。

- [ ] **真实终端里的 TUI 手工操作**：目前只有模型级渲染测试和 `tea.Program`（禁用输入）测试。尤其是：
  - 中文输入法下 Enter 是否在组合期误发送；
  - Windows 控制台里 Ctrl+C 是否按键事件送达；
  - 鼠标滚轮是否被送达（有的终端不上报）、Ctrl+Home/End 等组合键是否被识别；
  - 拖选复制：OSC 52 在各终端（含 SSH、tmux 里）是否生效，Linux 缺 xclip/xsel/wl-clipboard、Termux 缺 termux-clipboard-set 时的提示，反色高亮在各终端里的观感；
  - `─` `│` `•` `⚙` `└` 在 CJK 终端设置下是否按两列显示导致错位；
  - 流式渲染观感、窗口缩放。
- [ ] **OpenAI 官方服务**（Chat 与 Responses）：经第三方中转 true-sota 验证过 Chat（grok-4.5）与 Responses（gpt-6-sol）的文本、工具调用、识图、跨模型恢复（2026-11-20 实测）；没有直连 OpenAI 官方服务。
- [ ] **思维链 token 数**：Chat 协议下真实 DeepSeek 已确认返回 `completion_tokens_details.reasoning_tokens`（本机 11 次调用，与分词器数出的一致），中转的 grok-4.5 也返回；Gemini 的 `thoughtsTokenCount` 已确认，并据此修正了输出口径（`candidatesTokenCount` 不含思考，要加回）（2026-11-20 实测）。Responses 协议经中转的 gpt-6-sol 在 effort high 时 `reasoning_tokens` 仍为 0（直接请求原始接口也是 0，属于服务端行为），所以这一支还没见过非零值。不返回时 TUI 显示内核估算的“约 N token”。
- [ ] **Linux / macOS / Termux 实际运行**：CI 在 Linux / macOS 上跑单元测试（`bash` 探测、进程组终止等 Unix 路径仍以 fake probe 为主），但没有在这些系统上交互式地跑过 TUI 与真实模型；Termux 只验证过能用 `GOOS=android` 编译，CI 不覆盖。
- [ ] **状态栏的缓存读写**：经中转实测（2026-11-20 实测）：Chat（grok-4.5）的 `cached_tokens`、Responses（gpt-6-sol）的 `input_tokens_details.cached_tokens`、Anthropic（claude-sonnet-5-5）的读写两项都有真实数据，Anthropic 的输入口径（未命中 + 读 + 写）与响应对得上。没确认的：Gemini 的 `cachedContentTokenCount`（实测请求里没出现）、DeepSeek 的 `prompt_cache_hit_tokens`（账户余额不足，HTTP 402）。
- [ ] **状态栏的内存与 CPU**：只在 Windows 上运行验证过；Linux / Termux 只有解析器的单元测试（Android 8+ 读不了 `/proc/stat`，预期没有 CPU 一项），macOS 不显示这两项。是整机占用，进程内存那一项才是 sprout 自己的。系统负载 Linux 读 `/proc/loadavg`，Windows 用处理器队列长度模拟（只在 Windows 上验证过能读到），macOS 不显示。
- [ ] **`statusline.jsonc` 的终端实测**：布局解析、颜色校验、隐藏与行号调整都有单元测试，但没有在真实终端里跑过自定义布局（尤其是颜色名写法在各终端配色下的观感）。
- [x] **识图（M2）的真实服务商往返**：四种协议经中转各发了一张小图和一张 4000×3000 的大图（自动缩到 2000×1500），模型都认对了颜色和形状；Gemini 协议的文本、工具、识图也都在真实服务上跑通（2026-11-20 实测）。
- [x] **换模型后的历史重放**：Claude（开思考、带工具调用）开头的会话依次用 `-c` 换成 Responses、Gemini、Chat 再回到 Claude 恢复，每次都答对了前文的暗号和工具输出（2026-11-20 实测）。TUI 里 `/model` 当场切换走的是同一段重放逻辑，没在真实终端里点过。
- [ ] **双击 `sprout.exe` 启动**（含出错后等待回车）：用 `Start-Process` 模拟（新开控制台、进程独占）（2026-11-20 实测）：参数错误时进程停住等回车，正常启动时 TUI 保持运行；没有人眼看过窗口里的内容。
- [ ] **`sprout update` 的真实网络往返**：Windows 上已对真实发布完整替换过一次（2026-11-20 实测）：临时目录里版本号注入成 2026.11.12 的程序，下载 `v2026.11.19` 的 windows_amd64 产物、校验 `SHA256SUMS.txt`、试运行后替换成功，运行中的旧程序改名成 `.old`，下次启动时删掉。Termux / macOS / Linux 产物的平台匹配没在真机上确认。
- [ ] **`/mcp`、`/skills` 面板的真实终端操作**：只有模型级渲染与按键测试；`mcp.jsonc` / `skills.jsonc` 保留注释的写回用临时文件验证过，没有对用户手写的复杂配置做过大量实测。
- [ ] **每轮统计行**：只有模型级测试，没在真实终端和真实模型上看过数字。
- [ ] **`models.jsonc` 热加载**：用两种真实写法在 app 层验证过（2026-11-20 实测）：PowerShell 5.1 `Set-Content`（先清空再写、带 BOM）和写临时文件再改名（vim 式原子保存），都能读到新模型。没在 TUI 里配合 VS Code、记事本实际保存过。
- [ ] **`/install-sprout` / `/uninstall-sprout` 的真实效果**：Windows 的流程用注册表替身和临时目录测试过，读取真实用户 / 系统 PATH 只做过只读演练，没有真正写过注册表、没有确认新开的终端能直接敲 `sprout`；Linux / macOS / Termux 的符号链接逻辑只用文件系统替身测试过，真实系统上没跑过。
- [ ] **剪贴板取图**：Windows 的读取已对真实剪贴板验证（2026-11-20 实测）：`PNG` 格式原样取到、只有 `CF_DIB` 时转成 png、资源管理器式复制文件（`CF_HDROP`）取到路径、没有图片时返回空。没在真实终端里按过 Alt+V / Ctrl+V，Windows Terminal 拦下 Ctrl+V 后是否送来空粘贴没确认；macOS（osascript）与 Linux（wl-paste / xclip）的实现完全没在真机上跑过。
- [x] **排队消息在工具执行完后送达**：四种协议经中转在内核层实测（2026-11-20 实测）：工具执行完后接进“最终回答时把数字乘以 2”的补充要求，模型都按补充要求回答。TUI 里的显示只有模型级测试。
- [x] `go test -race`：本机 `D:\DevKit\mingw64` 有 gcc，`CGO_ENABLED=1 go test -race ./...` 全部通过（2026-11-20 实测）；第一次跑查出 MCP 后台连接与界面共用服务端列表的数据竞争，已修复。

## 三、未实现

### 后续里程碑（见 README 路线图，需用户确认后再做）
- [x] **里程碑编号**：已统一，以 `docs/m2-vision.md` 为准——M2 是识图（已实现），JSON-RPC 顺延为 M3，WebUI 为 M4，扩展为 M5。README 路线图与 `AGENTS.md` 已同步。
- [x] **JSON-RPC（M3）**（`internal/rpc`，协议见 `docs/rpc.md`；已用真实模型经脚本客户端跑通 `initialize`、带工具调用的 `turn/start`、`shutdown`（2026-11-20 实测），没接过真实的第三方客户端）：
  - [x] `sprout serve`（stdio、逐行 JSON-RPC 2.0）、`initialize`、`turn/start`、`turn/cancel`、`event` / `notice` 通知、`session/new`、`shutdown`；
  - [x] `session/list`、`session/resume`（返回重放事件）、`session/rename`、`session/delete`、`session/compact`（可取消）；
  - [x] `model/list`、`model/switch`、`effort/list`、`effort/set`（一轮进行中也能调档位）。
- [ ] **WebUI（M4）**（`internal/webui`，占位）：独立前端模块，复用 JSON-RPC 事件；`sprout web`。
- [ ] **扩展（M5）**：
  - [x] Anthropic Messages 协议（`internal/provider/anthropic`，已实现并测试）；
  - [x] Google Gemini 协议（`internal/provider/google`，已实现并测试）；
  - [x] MCP 客户端与工具接入（`internal/agent/mcp`）：stdio 与 Streamable HTTP 两种传输、无状态 `server/discover` 与旧版 `initialize` 双协议、配置 `mcp.jsonc`、工具注册为 `mcp__<服务端>__<工具>`；后台连接（启动不等服务端）与协议版本缓存（`mcp-versions.json`），见 `docs/mcp.md`；
  - [x] skills 发现与加载（`internal/agent/skills`）：扫描四个位置、解析 frontmatter、元数据追加进系统提示词、正文按需 `read`，见 `docs/skills.md`；
  - [x] skills 注册表（`skills.jsonc`）：只有注册的技能才生效，每条含 `path` 与 `enable`，`path` 可指向任意位置；扫描到的技能自动登记，已有条目不被覆盖；`scanAgents` 控制是否加载 `.agents/skills`；旧版 `{"disabled": [...]}` 启动时自动迁移；
  - [x] 技能管理界面：`/skills` 面板开关技能、切换 `scanAgents`、重新扫描；`/技能名 任务` 手动触发；
  - [x] 子代理（`internal/agent/subagent`，见 `docs/subagents.md`）：内置工具 `agent`，同步、只有一层，同一批调用并行；内置 general / explore 两种类型，`<数据目录>/agents/*.md` 与 `<工作目录>/.sprout/agents/*.md` 自定义类型（`tools` 通配筛选、`model`、`effort`）；子代理进度以 `subagent` 事件转发给 TUI 卡片、`sprout run` 的 stderr 与 RPC；子代理过程记在 `<会话名>.agents/<call id>.jsonl`，主会话记 `subagent_usage`，恢复后花费不丢。真实模型（claude-sonnet-5-5）派出 explore 子代理列目录并汇报已跑通（2026-11-20 实测）；TUI 卡片没在真实终端里看过。
  - [ ] 后台子代理（派出后先返回、之后再取结果或追加消息，类似 codex 的 spawn / send / wait）；子代理换了模型时花费按它自己的单价折算（目前按主模型单价）；
  - [ ] 搜索、web 等更多工具（现有 `bash`、`powershell`、`read`、`write`、`edit`、`delete`，外加 `tools.jsonc` 里定义的外部命令工具）；
  - [ ] 权限模块（审批流、allow/deny、沙箱）——目前默认完全权限，不要提前加。

### M1 范围内有意没做
- [x] 会话树：所有分支存在同一个会话文件里（`branch` 记录），TUI `/tree` 树形面板切换或改问某一轮，RPC `session/tree` / `session/navigate`（见 `docs/session-format.md`「分支与会话树」）。
- [x] 会话压缩的细化：按 token 精确裁剪、分层摘要、手动 `/compact` 均已完成。按工具类型区分裁剪策略（策略版本 2：`read` 只留开头并提示可重读，`agent` 开头结尾多留，`mcp__*` 留开头，shell 与外部命令留开头和结尾；旧会话按版本 1 重放）与手动压缩保留最近一轮原文（`/compact -k`、RPC `keepLast`）也已完成，见 `docs/m1-implementation.md` §15。
- [x] 按工具声明“是否可并行”：工具实现 `tool.Parallelizable` 声明，不可并行的调用独占执行（等前面的结束、执行时不启动别的）；`write`、`edit`、`delete` 声明为不可并行，其余默认可并行；`tools.jsonc` 的 `parallel` 可覆盖（见 `docs/tools.md`「并行」）。
- [x] 模型选择器、运行中切换模型：`/model`。
- [x] `花费`：单价来自 models.dev（`sprout models update` 下载到 `<数据目录>/models.dev.json`，启动时只读缓存、不联网）。每次用量到达时按当时模型的单价累加，切换模型不重算历史；`/new` 与恢复会话清零，恢复时按当前模型的价格重算重放出来的用量。没有价格时显示 `—`，一部分用量没价格时显示 `≥` 加金额。**已知限制**：只用基础价，忽略 models.dev 的分档价（`tiers`、`context_over_200k`）；价格没有和真实账单核对过。
- [ ] 状态栏里还是占位的：`插件`（没有插件机制）。
- [x] MCP 的其他能力（细节与剩余限制见 `docs/mcp.md`）：
  - [x] 工具结果里的图片转成图片块发给模型；
  - [x] Resources：`resources/list`、`resources/templates/list`、`resources/read`；
  - [x] Prompts：`prompts/list`、`prompts/get`，注册为 `list_prompts` / `get_prompt` 工具；
  - [x] Elicitation：服务端发起的 `elicitation/create`（只支持 form 模式），TUI 弹表单，`sprout serve` 转发给声明了能力的 RPC 客户端（`mcp/elicit`）；
  - [x] MRTR：`tools/call`、`resources/read`、`prompts/get` 的 `input_required` 补充输入后重发，最多 8 轮；
  - [x] Sampling：服务端发起的 `sampling/createMessage`，用户确认后用当前模型生成，`sprout serve` 转发（`mcp/sample`）；
  - [x] OAuth：`sprout mcp login/logout`，PKCE、受保护资源与授权服务器元数据发现、动态注册、浏览器授权与回环回调、令牌存储（`mcp-auth.json`）与刷新。**未验证**：没有对接真实 OAuth 服务商、没用真实浏览器跑过，非 Windows 平台的浏览器打开未测；不支持 CIMD。
- [x] MCP 工具列表缓存：`<数据目录>/mcp-tools.json`，有效期取服务端的 `ttlMs`（缺省 10 分钟，`0` 表示不缓存），按连接目标指纹失效；`notifications/tools/list_changed` 收到后后台重新列工具，TUI 空闲时重建 agent。
- [x] 技能正文的裁剪保护：`read` 读取 `SKILL.md` 的工具结果不参与旧输出裁剪（按历史里的调用参数判断，会话恢复重放结果一致）。
- [x] 每轮回答结束后的统计行：回答下方一行淡色的 `耗时 · 首字 · token/s · 输入 · 输出 · 缓存 · 合计`，是这一轮主对话全部模型调用的合计（不含子代理）；恢复会话时只写 token（见 `docs/m1-implementation.md` §9）。
- [x] 配置热加载（TUI）：`models.jsonc` 改完几秒内自动重新读取，`/model` 列表随之更新，当前模型的设置变了就在空闲时重建 agent、保留对话；写错时继续用旧配置并提示。`sprout run`、`sprout serve` 和缺 API Key 启动时仍要重启（见 `docs/m1-implementation.md` §5）。
- [x] 剪贴板取图：TUI 里 Ctrl+V / Alt+V 粘贴剪贴板里的图片或复制的图片文件，显示成 `[剪贴板图片 N]`（见 `docs/m2-vision.md` §6）。
- [x] 交互式 CLI：`sprout run -i` 一行一问、行尾 `\` 续行、生成中 Ctrl+C 只取消当前回答；`sprout run` 不写提问时从 stdin 读，单独的 `-` 把 stdin 接在提问后面（见 `docs/cli.md`）。
- [x] 思考程度调节：`/effort` 档位条与 `--effort`，档位由 `models.jsonc` 的 `effort` 按模型声明；四种协议按各自真实字段发送，Anthropic 思考块带签名原样回传（`docs/effort.md`）。
- [x] Responses 协议下的思考摘要：`reasoning.summary=auto` 时接收 `reasoning_summary_text` 并显示。
- [x] Responses 协议回传 reasoning 条目：开启思考时请求 `include: reasoning.encrypted_content`，把 reasoning 条目原文存进 `Message.Native`，下一轮放在同一条 assistant 的正文与 `function_call` 之前回传。
- [x] Gemini 3 的 `thoughtSignature` 回传：带签名的 part 存进 `Message.Native` 原样回传；纯思考摘要不回传，相邻逐字文本合并成一个 part。
- [x] 按模型切换 `max_tokens` / `max_completion_tokens`：`models.jsonc` 的 `maxTokensParam`，只对 Chat 协议生效。
- [x] 会话与日志的清理：日志只留 30 天（启动时删掉更早的 `sprout-YYYYMMDD.log`）；会话默认不自动删，`sprout sessions clean --older-than 30d [--dry-run]` 手动清理，`settings.jsonc` 的 `cleanupPeriodDays` 打开启动时自动清理（见 `docs/cli.md`）。

## 四、已知限制与风险

- **环境变量泄露给模型**：工具子进程继承 `sprout` 的环境变量，包括 `DEEPSEEK_API_KEY` 等；模型可以让命令读到它。没有权限模块前是已知取舍。
- **项目级技能是提示注入面**：克隆来的陌生仓库可能带着 `.agents/skills/` 或 `.sprout/skills/`，其中的技能正文会进上下文。目前没有信任门槛，只建议在可信仓库里使用项目级技能。
- **项目级子代理定义同样是提示注入面**：`.sprout/agents/*.md` 的正文会进子代理的系统提示词，和项目级技能一样没有信任门槛。`explore` 的只读也只是提示词约束，shell 仍能写文件。
- **非终端启动 TUI 不退出**：stdin 重定向到 `/dev/null` 时 TUI 会一直挂到被外部终止。
- **思维链回传**：DeepSeek 文档要求带 `tools` 的请求回传历史 `reasoning_content`，已实现；实测当前服务端尚未强制，之后可能变严。
- **`max_tokens`**：直接取 `models.jsonc` 的 `maxOutput`；Chat 协议可用 `maxTokensParam` 换成 `max_completion_tokens`（o 系列等）。
- **并行工具的前提**：同批调用并行、互相看不到结果，靠系统提示词让模型把有依赖的命令合并或分回合；模型不听话时（比如先建目录再写文件放同一批）会得到失败结果，由它自己纠正。`write` / `edit` / `delete` 独占执行，同批里改同一个文件不会互相覆盖，但 shell 命令之间仍然并行。工具实现必须并发安全。
- **token 估算是近似值**：按字符类别估算（英文单词每 5 个字母约 1 个、数字每 3 位 1 个、汉字 0.7、符号 1.5、emoji 2，见 `internal/agent/tokens.go`），用 DeepSeek V4 分词器在本机真实会话上校准，整体略微偏高（约 1.1 倍），并用服务端上次报告的用量校准。旧的 OpenAI cl100k 分词器（gpt-4、gpt-3.5）每个汉字约 1 个 token，中文多时会低估约三成。窗口配置错误（比实际大）时靠“服务端报超长后压缩重试”兜底。裁剪会改动历史前缀，使服务端的前缀缓存失效一次。
- **恢复会话不回放增量**：TUI 恢复时对话区由落盘的完整消息重建，看不到当初的逐字流式过程；思维链在恢复后是折叠摘要。
- **PowerShell 退出码**：`-Command` 模式下失败通常是 1，不一定等于原生程序自己的退出码（已写进工具描述）。
- **Windows 进程树终止**：用 `taskkill /T`，没有使用 Job Object。

## 五、待你决定

- [x] **会话路径与 `AGENTS.md` 不一致**：已定以实际实现为准，`sessions/--<cwd slug>--/<时间戳>_<id>.jsonl`；`AGENTS.md` 决策表与 `README.md` 已同步。
- [x] **`meta.json` 语义**：已改为参照 pi / Claude Code / opencode，元数据跟着会话文件走（`session` 头加 `name` 记录），不再写分组索引；见 `docs/session-format.md` §4。
- [x] **是否提供关闭思考的开关**：已由 `/effort off` 实现，模板里 DeepSeek 的 off 档位发送 `thinking: {"type":"disabled"}`。
- [x] **按模型切换 `max_tokens` / `max_completion_tokens`**：已由 `models.jsonc` 的 `maxTokensParam` 实现。
- [x] **输出上限的默认值**：`deepseek-flash` 的 `maxOutput` 保持官方上限 384000，不改。

## 六、工程与文档

- [x] CI：`.github/workflows/ci.yml` 在 Windows / Ubuntu / macOS 上跑 `go build` / `go vet` / `go test`，另有一个 job 在 Linux 上交叉编译全部发布目标（只验证能打包，不上传）。是否通过以 GitHub 上的运行结果为准，本文件不记录。
- [x] LICENSE：Apache-2.0，发布包一并附带。
- [x] `README.md` 的“项目结构”已补上 `tool/shell`、`tool/file`、`tool/external`、`media`、`provider/google`、`provider/transport`，`skills`、`mcp`，并标出 `webui` 仍是占位（`rpc` 已实现）。
- [x] `README.md` 与 `AGENTS.md` 的“当前状态”已更新为 M1 + M2，协议表补上 Gemini，深入文档列表补全。
- [x] `docs/slash-commands.md` 已列出 `/new` 与 `/image`。
- [x] `docs/cli.md`、`docs/keybindings.md`、`docs/slash-commands.md` 已提交到 git。
- [x] 事件协议、会话格式、决策记录已单独成文：`docs/events.md`、`docs/session-format.md`、`docs/decisions.md`；`m1-implementation.md` §10、§15 与 `m2-vision.md` §3 加了指向新文档的说明。
- [x] 测试里的旧模型名 `deepseek-chat` 已统一为模板中的 `deepseek-flash`（`session_test.go`、`tui/helpers_test.go`、`tui/model_test.go`）。


