# M2：视觉能力（图片输入）

M2 在 M1 的 ReAct 闭环之上加入图片输入：四种协议都能把图片发给模型，会话能完整保存和恢复图片，
不支持视觉的模型会自动降级。M2 只做「输入」，不做图片生成、不做 OCR 工具、不做图像编辑。

## 1. 设计取向

参照 Claude Code 的模型，而不是给 `Message` 加一个旁路的 `Images []Image` 字段：

- **内容是一串块**。`Message.Content` 从字符串改成 `[]Block`，text 与 image 按原顺序排列。
  图文交错的语义不丢（「图一是什么，图二有什么不同」这种提问必须能还原顺序），
  而且以后加文件、音频块不需要再改一次消息模型。
- **工具结果也能带图片**。截图、图表渲染这类工具的输出天然是图片。
- **图片落盘，会话里存引用**。base64 直接写进 JSONL 会让单行几 MB，`/resume` 列表的逐行扫描也会变慢；
  会话本来就是长期保留的，图片作为独立文件存进会话目录更合适。
- **不支持视觉的模型降级，而不是报错**。用户切到没有视觉能力的模型后，历史里的图片变成占位文本，
  对话仍可继续；历史本身不改，切回视觉模型还能看到原图。

## 2. 内部模型（`internal/agent`）

```go
type Block struct {
    Type      string // text | image
    Text      string // text
    MediaType string // image：如 image/png
    Data      string // image：base64（不含 data: 前缀）
}

type Message struct {
    Role      Role
    Content   []Block
    Reasoning string
    ToolCalls []ToolCall
    ToolID    string
}

type Image struct { Name, MediaType, Data string } // 一次输入携带的图片
type Input struct { Text string; Images []Image }

func (m Message) Text() string               // 全部文本块拼接：展示、摘要、裁剪、估算都用它
func (m Message) HasImages() bool
func (m Message) WithoutImages(placeholder string) Message // 图片换成占位文本，连续图片只留一个占位
```

- `Agent.Run(ctx, Input{Text, Images}, sink)`；`Input.Message()` 把文本放前、图片按顺序跟在后面，
  纯图片提问不补空文本块（Anthropic / Gemini 不接受空 text block）。
- assistant 消息的空文本不产生文本块，会话重放用同样的规则，保证恢复出的历史与运行时逐条相等。

## 3. 事件与会话

> 事件与会话记录的当前完整格式见 `docs/events.md`、`docs/session-format.md`。

事件：`event.TurnStarted` 增加 `Images []ImageInfo`，每项带 `Name`、`MediaType`、`Bytes`、`Data`（base64），
以及落盘后回填的 `Path`。

会话记录器在写 `user` 记录前把图片写成文件：

- 目录：与 JSONL 同级的 `<会话名>.images/`，文件名取内容 sha256 前 8 字节 + 按媒体类型的扩展名。
  同一张图重复发送只存一份。
- `user` 记录新增 `images: [{file, media_type, bytes}]`，`file` 是相对会话文件的路径，
  整个会话目录搬走也能恢复。JSONL 里不内联 base64。
- 记录器会先复制一份 `Images` 切片再清空 `Data`：事件按值分发给多个 sink，底层数组是共享的，
  直接改会和正在读它的 TUI 产生数据竞争。
- 图片写失败时整条 `user` 记录不写（并把记录器标记为失败），避免重放出的历史与运行时不一致。
- 旧会话没有 `images` 字段，正常读取。

恢复：`session.Load` 按 `file` 读回图片文件，转回 `image` 块；文件被删或损坏时换成
「（图片无法恢复：路径）」文本块，不让整轮无法恢复。`/resume` 列表的首条提问后加 `[图片]` 标记。

## 4. 协议映射

| 协议 | user 消息 | 工具结果里的图片 |
| --- | --- | --- |
| Chat Completions | `content` 从字符串改为 parts 数组：`{"type":"text"}` / `{"type":"image_url","image_url":{"url":"data:..."}}`；纯文本仍发字符串 | tool 消息只能带文本：图片攒到这组 tool 消息之后，用一条 user 消息补发 |
| Responses | `content` 用 `input_text` / `input_image`（data URL）数组；纯文本仍发字符串 | `function_call_output.output` 只能是字符串：同样在其后用 user 消息补发 |
| Anthropic Messages | text / image block 数组，图片为 `{"type":"image","source":{"type":"base64",...}}` | `tool_result.content` 从字符串改为 text / image block 数组（协议原生支持） |
| Gemini | part 数组，图片为 `{"inlineData":{"mimeType","data"}}` | `functionResponse.response` 必须是 JSON 对象：结果文本注明「另附 N 张图片」，图片在同一组 functionResponse 之后用 user 消息补发 |

补发图片的那条 user 消息带一句说明文字（「（以上工具结果附带的图片）」），让模型知道图片属于哪个结果。

## 5. 降级与预算

- `models.jsonc` 的模型新增 `"vision": true`；缺省为 `false`。未标注的模型收到图片时，
  发给服务端的请求里图片被替换成「（图片已省略：当前模型不支持图片输入）」，历史保持原样。
- 支持识图时系统提示词会明说「消息里附带的图片你已经能看到，不要用工具去读取或猜测」。
  没有这句时，带路径的提问会被当成文件任务，模型会去 `Test-Path` / 找 OCR，而不是直接看图
  （Claude Code 用 `[Image #N]` 占位、pi 用 `(see attached image)` 文本占位，也是同一个目的）。
- TUI 在附加图片时、CLI 在 `--image` 时都会提示当前模型没有标注 `vision`。
- 图片 token 估算：能解出尺寸就按 `宽×高 / 750` 折算并夹在 `[300, 8000]` 之间，
  解不出尺寸时按 1600 计（`internal/media` 注册了 webp 解码器，程序里 webp 也能读出尺寸）。带图片的 tool 消息不参与旧输出裁剪。

## 6. 入口

- TUI：`/image 路径` 附加一张（可多次），`/image` 查看待发送列表，`/image clear` 清空；
  发送时随消息一起提交，用户条目上方显示 `[文件名] 大小`。`/help` 已列出。
- 粘贴内容里的图片路径当场换成绿色的 `[文件名]`（输入框和对话区一致）：粘贴时就读取并附加图片，
  发送时再把标记换回原始路径，模型拿到的仍是用户粘贴的原文；用户删掉标记就不再发送那张图。
- **消息里直接写图片路径也会自动附加**：`...\截图.png 这张图的内容` 会把那张图真的发给模型，
  而不是只把路径当普通文字（否则模型只能自己去 `Test-Path` 或者找 OCR，等于没识图）。
  识别规则（`media.FindImagePaths`）：只认**确实存在**的图片文件；先取引号内的整段（带空格的路径），
  再在空白切分的片段里从盘符、每个分隔符之后的位置当起点逐字符回退，所以「图：D:.png 这张」这种
  中英混排也能切出来。路径文本原样保留；一条消息最多自动附加 10 张；读失败的（超限、损坏）不阻塞发送，
  只给出原因。
- **剪贴板取图**：TUI 里按 Ctrl+V 或 Alt+V（Windows Terminal 会拦下 Ctrl+V，用 Alt+V），终端拦下 Ctrl+V 后送来空粘贴时也会去读。
  读取在 `platform.ClipboardImage`：Windows 用 Win32 剪贴板 API（先取 `PNG` 格式，没有再取 `CF_DIB` 自己转成 png，
  资源管理器里复制的文件取 `CF_HDROP`）；macOS 用 `osascript` 取 `«class PNGf»` 或 Finder 复制的文件；
  Linux / BSD 用 `wl-paste`（Wayland）或 `xclip`（X11）先列类型再取图片或 `text/uri-list`；Termux 不支持。
  图片在输入框里显示成 `[剪贴板图片 N]`，图片的 `Source` 就是这个标记，所以它原样留在正文里发给模型，
  告诉模型图片在哪；删掉标记就不发这张图。复制的是文件时按粘贴这些路径处理。Ctrl+V 读不到图片时退回粘贴文字。
- CLI：`sprout run --image a.png --image b.png "看图"`，可重复；只给图片、不给文字也可以。
  提问文本里的图片路径同样自动附加，并在 stderr 提示附了哪些。
- 图片来源统一走 `internal/media`：读文件、按内容嗅探类型（不信扩展名），再按下一节的规则缩放压缩。

## 6.1 缩放与压缩（`media.Prepare`）

- 发给模型的单张图片不超过 3 MB（base64 后约 4 MB，在常见服务商 5 MB 限制以内），长边不超过 2000 像素
  （服务商收到更大的图也会在服务端缩小；Anthropic 在一次请求图片较多时直接拒绝长边超过 2000 的图）。
- 两项都在上限内时**原样发送**，不重新编码：画质、gif 动图、webp 都不变。
- 超出时解码、用 CatmullRom 等比缩到长边 2000，再重新编码：原图不是 jpeg 的（截图、图表）先试 png，
  超限再用 jpeg；原图是 jpeg 的直接用 jpeg（质量 85，不够再 70）。仍然超限就把长边再缩到 3/4，最多四档。
  透明部分在 jpeg 里铺白底；gif 动图只保留第一帧。
- jpeg 的 EXIF 方向标记（手机照片常见）在重新编码前先应用到像素上，因为重新编码会丢掉 EXIF。
- 读入的原图上限 64 MB、8000 万像素，再大不处理；文件头像图片、内容却解不开的，在 3 MB 内原样发送交给服务端判断，
  超过 3 MB 就报错。
- 缩放一张千万像素的照片约 1 秒，TUI 的 `/image`、粘贴路径和剪贴板取图都在后台读，读的时候界面照常响应；
  提交时自动附加（`AutoAttach`）和 `sprout run --image` 仍是同步的。
- `name` 保留原文件名（输入框里的 `[文件名]` 标记靠它对应），媒体类型以重新编码后的为准。

## 7. 不做 / 未验证

- gif 在上限内原样发送，不拆帧；需要缩放时只保留第一帧。
- 各协议的真实服务端图片往返只用本地 `httptest` 验证了请求形状，**尚未对真实服务商接口做手工验证**。
