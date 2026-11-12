# TODOS

M1（最小可用闭环）已实现并提交。本文件区分三类状态：**已实现**、**已实现但未验证**、**未实现**。
验收清单的逐项状态见 `docs/m1-implementation.md` §12，与原计划的差异见 §15。

## 一、已实现

### 内核（`internal/agent`）
- [x] ReAct 循环：模型调用 → 工具执行 → 结果回传，最多 10 次模型调用（`max_steps`）。
- [x] 事件协议（`event`）：`turn.started` / `message.delta` / `reasoning.delta` / `message.completed` / `tool.requested` / `tool.finished` / `usage` / `retrying` / `compacted` / `turn.finished` / `error`；每轮必以 `turn.finished` 收尾。
- [x] 取消：`context` 贯穿 HTTP 请求与工具进程；取消时为未执行的 tool call 补“未执行”结果，保证历史合法。
- [x] 失败回滚：首次模型调用失败/取消时撤销本轮用户消息；panic 被 recover 并还原历史。
- [x] 空工具参数规范化为 `{}`；工具的任何失败都转成 tool 消息回给模型，不终止 agent。
- [x] 系统提示词只描述实际可用的工具；两个 shell 都可用时说明选择规则。
- [x] 思维链：保存 `reasoning_content` 并在后续请求中回传。
- [x] 并行工具：同一批 tool call 并发执行（`MaxParallelTools` 默认 4，1 为串行）；事件与历史写入留在主 goroutine，历史里的 tool 消息始终按调用顺序；工具 panic 只算该次调用失败。真实 DeepSeek 验证过两个各睡 4 秒的命令重叠执行。
- [x] 自动重试：网络失败、408/409/425/429/5xx、流被截断按指数退避重试（默认 3 次、±25% 抖动），听 `Retry-After`（超过 30 秒不等）；`insufficient_quota` 不重试；本次调用已向界面输出内容后不再重试，避免重复输出；退避期间可取消。
- [x] 上下文管理：`contextWindow` 接近预算时先裁剪旧工具输出，不够再把更早的对话总结成摘要（只在用户消息处切，不拆 tool call 与结果）；服务端报告超长时压缩一次再重试；摘要失败不丢历史；每次压缩都有事件和提示。真实 DeepSeek 验证过（调小窗口后连续压缩 4 次、暗号未丢）。
- [x] 会话恢复：`-c` / `--resume`（`run` 与 TUI 都支持）和 TUI 的 `/resume`；重放规则与运行时一致（并行工具结果按调用顺序、被杀留下的未完成调用补“未执行”、首次调用失败撤销用户消息、压缩按当初方式重做），换模型时丢弃旧思维链；恢复后接着写原会话文件。真实 DeepSeek 验证过 `run -c` 能记住上文。
- [x] 会话浏览器：TUI 的 `/resume` 替换输入框列出会话，可切当前目录 / 全部、搜索（关键词 / `re:` 正则 / `"短语"`）、按最近 / 最早 / 轮数排序、只看已命名、重命名（存在 `meta.json` 的 `name`）、删除（连同图片，需确认，当前会话不能删）。

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
- [x] 平台探测：按文档顺序查找；`System32\bash.exe`（WSL 启动器）判为不可用并提示安装 Git for Windows。
- [x] 执行契约：每次新进程、工作目录固定、默认超时 120s（上限 600s，模型可传 `timeout_seconds`）、输出上限 50KB（头尾保留）、stdin 置空。
- [x] 超时/取消终止整个进程树（Unix 进程组，Windows `taskkill /T`），`WaitDelay` 防止孙进程占管道。
- [x] PowerShell 注入 UTF-8 输出编码前导；工具描述按 edition 区分语法。

### 配置与数据目录（`internal/agent/config`）
- [x] `models.jsonc`（JSONC，注释与尾逗号）；语法/类型错误定位到 `文件:行:列`（列按字符计）。
- [x] 校验一次汇总所有问题；API Key 环境变量优先、`apiKey` 兜底；`tools:false` 的模型不下发工具。
- [x] 数据目录 `--data-dir` > `SPROUT_DATA_DIR` > `<exeDir>/data/.sprout`；`go run` 临时二进制或不可写时报错，不回退 `~/.sprout`。
- [x] 首次运行生成带注释的模板（不覆盖已有文件）；模板与 `models.example.jsonc` 由测试保证一致。

### 会话（`internal/agent/session`）
- [x] JSONL 记录器（`event.Sink`）：`session` / `user` / `assistant` / `tool` / `usage` / `turn` / `error`；文件懒创建，权限 0600。
- [x] 路径 `sessions/--<cwd slug>--/<时间戳>_<id>.jsonl`；同目录 `meta.json` 为会话索引（原子替换，损坏重建）。

### TUI（`internal/tui`）
- [x] 全屏 Bubble Tea v2；`Update` 不做 IO，agent 在 `tea.Cmd` 的独立 goroutine 里运行。
- [x] 流式输出、工具卡片（命令、退出码、耗时、最多 8 行预览）、输出里的 ANSI/控制字符清理。
- [x] 历史滚动：鼠标滚轮、PgUp/PgDn、Ctrl+↑↓、Ctrl+Home/End；输入框为空时 ↑↓ Home End 同样滚动；状态行显示位置与“有新内容”。
- [x] Markdown（CommonMark + GFM）：标题、嵌套列表与任务列表、多级引用、分隔线、围栏代码块（chroma 语法高亮）、粗体/斜体/删除线/行内代码、链接与图片、表格（按显示宽度对齐、列对齐标记、过宽时缩列折行、极窄时逐行展开）。流式输出中途未闭合的语法原样显示。
- [x] 思维链淡色显示：流式预览最后 3 行；结束后折叠成一行“已思考 N秒 N token：内容…”，点击单条展开或收起，`/think` 一次切换全部。思维链 token 取服务端报告值（未报告时显示内核估算并标“约”）。token 数缩写为 2.9k、12k 这样。
- [x] `/help` `/tools` `/think` `/mouse` `/model` `/effort` `/new` `/image` `/resume` `/install-sprout` `/uninstall-sprout` `/quit`；缺 API Key 时禁止发送并显示配置提示。
- [x] 斜杠命令补全：输入 `/` 列出候选，Tab 填入、↑↓/Ctrl+P/N 选择、Esc 关闭；`/effort` 与 `/model` 的参数也参与补全。命令与快捷键说明见 `docs/斜杠命令.md`、`docs/快捷键.md`。
- [x] `/model` 模型选择器：↑↓ 或直接输入过滤，Enter 切换；保留对话历史、清空旧模型思维链；生成中或恢复/切换中拒绝。
- [x] `/new` 清空对话区与上下文开始新会话，旧会话仍可用 `/resume` 找回。
- [x] `/image 路径` 附加图片，粘贴或消息里直接写图片路径时自动附加（`internal/media`、`internal/tui/image.go`）。
- [x] 对话区拖选复制：按住左键拖动选中（反色显示），松开自动复制；选区跟着内容走，拖到上下边缘自动滚动；中文按整个字选中；内容重排（窗口缩放、`/think` 展开）导致选区对不上时自动取消。复制同时走 OSC 52 和系统剪贴板，任何一条失败只提示、不当作致命。
- [x] 真实终端光标（输入法候选窗跟随）；退出时 `Shutdown` 等 agent 收尾，保证 `turn.finished` 被记录。

### 识图（`internal/media`，设计见 `docs/m2-vision.md`）
- [x] `Message.Content` 改为 text / image 块数组；工具结果也能带图片。
- [x] 四种协议都能发图；协议不支持在工具结果里放图时，在其后补一条 user 消息。
- [x] 会话把图片存成 `<会话名>.images/` 下按内容哈希命名的文件，JSONL 里只存相对路径；恢复时读回，文件丢失换成占位文本。
- [x] `models.jsonc` 的 `vision` 标记；未标注的模型发送时图片换成占位文本，历史不改。
- [x] 图片 token 估算（按尺寸折算，夹在 300–8000）；单张上限 3 MB，按内容嗅探类型。

### CLI 与入口（`internal/cli`、`cmd/sprout`）
- [x] `sprout`（TUI）、`sprout run "..."`（答案写 stdout，进度/错误写 stderr；退出码 0 / 1 / 2 / 130）。
- [x] `--effort`、`--image`（可重复）、`-c` / `--resume`；完整说明见 `docs/CLI命令.md`。
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
- [ ] **OpenAI 官方服务**（Chat 与 Responses）：只验证过 DeepSeek。Responses 的 `function_call` 不带 `id` 是依据文档推断，带 reasoning 的 gpt-5 / o 系列可能不同。
- [ ] **思维链 token 数**：Chat 协议下真实 DeepSeek 已确认返回 `completion_tokens_details.reasoning_tokens`（本机 11 次调用，与分词器数出的一致）；Responses 协议的 `output_tokens_details.reasoning_tokens` 和 OpenAI 官方服务只有 `httptest` 测试。不返回时 TUI 显示内核估算的“约 N token”。
- [ ] **Linux / macOS / Termux 实际运行**：只验证过能编译；`bash` 探测、进程组终止等 Unix 路径只有单元测试（fake probe）。
- [ ] **识图（M2）的真实服务商往返**：四种协议的图片请求形状只有 `httptest` 验证，没有对真实服务商手工发过图；Gemini 协议整体也只用测试替身验证过。
- [ ] **`/model` 切换与恢复后的历史重放**：换模型时丢弃旧思维链、保留正文的逻辑有单元测试，但没有在真实多模型服务上跑过。
- [ ] **双击 `sprout.exe` 启动**（含出错后等待回车）。
- [ ] **`/install-sprout` / `/uninstall-sprout` 的真实效果**：Windows 的流程用注册表替身和临时目录测试过，读取真实用户 / 系统 PATH 只做过只读演练，没有真正写过注册表、没有确认新开的终端能直接敲 `sprout`；Linux / macOS / Termux 的符号链接逻辑只用文件系统替身测试过，真实系统上没跑过。
- [ ] `go test -race`：本机没有 gcc，未跑过。

## 三、未实现

### 后续里程碑（见 README 路线图，需用户确认后再做）
- [ ] **里程碑编号与 `docs/m2-vision.md` 冲突**：README 路线图把 M2 定为 JSON-RPC、M3 为 WebUI；`docs/m2-vision.md` 把“M2”用于视觉（识图，已实现）。需要统一编号后再继续写路线图。
- [ ] **JSON-RPC**（`internal/rpc`，目前只有占位目录）：会话管理、流式事件、取消；`sprout serve`。
- [ ] **WebUI**（`internal/webui`，占位）：独立前端模块，复用 JSON-RPC 事件；`sprout web`。
- [ ] **扩展**：
  - [x] Anthropic Messages 协议（`internal/provider/anthropic`，已实现并测试）；
  - [x] Google Gemini 协议（`internal/provider/google`，已实现并测试）；
  - [ ] MCP 客户端与工具接入（`internal/agent/mcp`，占位）；
  - [ ] skills 发现与加载（`internal/agent/skills`，占位）；
  - [ ] 子代理 / 多 agent 编排；
  - [ ] 文件读写、搜索、web 等更多工具（现在只有 `bash` 和 `powershell`，其余由用户后续决定）；
  - [ ] 权限模块（审批流、allow/deny、沙箱）——目前默认完全权限，不要提前加。

### M1 范围内有意没做
- [ ] 会话分支（恢复和会话搜索已实现，见 `/resume` 会话浏览器）。
- [ ] 会话压缩的细化：按 token 精确裁剪、分层摘要、让用户手动触发 `/compact`；目前只有自动触发。
- [ ] 按工具声明“是否可并行”：现在同批调用一律并行（上限 4），靠系统提示词要求模型不要把有依赖的命令放同一批。
- [x] 模型选择器、运行中切换模型：`/model`。
- [ ] 配置热加载：改 `models.jsonc` 仍需重启才生效，`/model` 只列启动时读到的模型。
- [ ] 剪贴板取图（`Ctrl+V` 粘贴图片）、图片缩放与降采样：超过 3 MB 直接拒绝（见 `docs/m2-vision.md` §7）。
- [ ] 交互式 CLI REPL；`sprout run` 不支持从 stdin 读提问。
- [x] 思考程度调节：`/effort` 档位条与 `--effort`，档位由 `models.jsonc` 的 `effort` 按模型声明；四种协议按各自真实字段发送，Anthropic 思考块带签名原样回传（`docs/effort.md`）。
- [x] Responses 协议下的思考摘要：`reasoning.summary=auto` 时接收 `reasoning_summary_text` 并显示。
- [ ] Responses 协议回传 reasoning 条目（需要 `include: reasoning.encrypted_content`），现在 `store:false` 下多轮工具调用拿不到上一步的思考。
- [ ] Gemini 3 的 `thoughtSignature` 回传：函数调用时要求带回，目前未保存；遇到相应报错时按 `Message.Native` 的方式补上。
- [ ] 会话与日志的清理/轮转（`sessions/`、`logs/` 只增不减）。

## 四、已知限制与风险

- **环境变量泄露给模型**：工具子进程继承 `sprout` 的环境变量，包括 `DEEPSEEK_API_KEY` 等；模型可以让命令读到它。没有权限模块前是已知取舍。
- **非终端启动 TUI 不退出**：stdin 重定向到 `/dev/null` 时 TUI 会一直挂到被外部终止。
- **思维链回传**：DeepSeek 文档要求带 `tools` 的请求回传历史 `reasoning_content`，已实现；实测当前服务端尚未强制，之后可能变严。
- **`max_tokens`**：直接取 `models.jsonc` 的 `maxOutput`。个别新 OpenAI 模型（o 系列）在 Chat 协议下拒绝 `max_tokens`，需改用 Responses 协议；没有按模型切换参数名的机制。
- **并行工具的前提**：同批调用并行、互相看不到结果，靠系统提示词让模型把有依赖的命令合并或分回合；模型不听话时（比如先建目录再写文件放同一批）会得到失败结果，由它自己纠正。工具实现必须并发安全。
- **token 估算是近似值**：按字符类别估算（英文单词每 5 个字母约 1 个、数字每 3 位 1 个、汉字 0.7、符号 1.5、emoji 2，见 `internal/agent/tokens.go`），用 DeepSeek V4 分词器在本机真实会话上校准，整体略微偏高（约 1.1 倍），并用服务端上次报告的用量校准。旧的 OpenAI cl100k 分词器（gpt-4、gpt-3.5）每个汉字约 1 个 token，中文多时会低估约三成。窗口配置错误（比实际大）时靠“服务端报超长后压缩重试”兜底。裁剪会改动历史前缀，使服务端的前缀缓存失效一次。
- **恢复会话不回放增量**：TUI 恢复时对话区由落盘的完整消息重建，看不到当初的逐字流式过程；思维链在恢复后是折叠摘要。
- **PowerShell 退出码**：`-Command` 模式下失败通常是 1，不一定等于原生程序自己的退出码（已写进工具描述）。
- **`meta.json` 并发**：读-改-写不加跨进程锁，两个实例同时在同一目录启动可能丢一条索引登记；JSONL 本身不受影响。
- **Windows 进程树终止**：用 `taskkill /T`，没有使用 Job Object。

## 五、待你决定

- [ ] **会话路径与 `AGENTS.md` 不一致**：`AGENTS.md` 决策表写 `sessions/<工作目录 slug>/<session-id>.jsonl`，实际按 `m1-minimal.md` 实现为 `sessions/--<cwd slug>--/<时间戳>_<id>.jsonl`。要改代码还是改决策表？
- [ ] **`meta.json` 语义**：现在是工作目录分组下所有会话的索引；如果你想要“每个会话一份元数据”，需要改格式。
- [x] **是否提供关闭思考的开关**：已由 `/effort off` 实现，模板里 DeepSeek 的 off 档位发送 `thinking: {"type":"disabled"}`。
- [ ] **按模型切换 `max_tokens` / `max_completion_tokens`**：要不要在 `models.jsonc` 里加字段，解决 o 系列在 Chat 协议下的参数名问题。
- [ ] **输出上限的默认值**：`deepseek-flash` 的 `maxOutput` 现为官方上限 384000，是否过大；不填则不发该参数。

## 六、工程与文档

- [ ] 没有 CI（无 `.github/`）：至少跑 `go build` / `go vet` / `go test`，最好覆盖 Windows 与 Linux。
- [ ] 没有 LICENSE（README 写的是 TBD）。
- [ ] `README.md` 的“项目结构”缺少 `internal/agent/tool/shell`（bash / powershell 共用的执行器）、`internal/media`、`internal/provider/google`、`internal/provider/transport`；`provider/` 的注释仍只写了 openai 与 anthropic。
- [ ] `README.md` 与 `AGENTS.md` 的“当前处于 M1”“M1 只有 OpenAI Chat + Responses”已过时，没有提到 Anthropic、Gemini 与识图。
- [ ] `docs/斜杠命令.md` 的命令表缺少 `/new` 与 `/image`（`/help` 里已有）。
- [ ] `docs/CLI命令.md`、`docs/快捷键.md`、`docs/斜杠命令.md` 尚未提交到 git。
- [ ] `AGENTS.md` 约定的架构 / 协议 / 决策记录文档还没有单独成文（事件协议和会话格式目前散落在 `m1-implementation.md` §10、§15 与 `m2-vision.md` §3）。
- [ ] 部分测试里仍用旧模型名 `deepseek-chat` 作为随意的字符串（`session_test.go`、`tui/helpers_test.go`），不影响功能，可顺手统一。


