package file

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/shumengya/sprout/internal/agent/tool"
)

// Edit 按原文片段替换文件里的一处或多处内容。
//
// 和 write 的区别：write 要模型先把整份文件复述一遍，长文件既费 token 又容易抄错；
// edit 只提交被改的那一段，改不到、改到多处都会明确报错，不会悄悄改错地方。
type Edit struct {
	dir string
}

func NewEdit(dir string) *Edit { return &Edit{dir: dir} }

func (t *Edit) Name() string { return "edit" }

func (t *Edit) Description() string {
	return "把文件里的 old_string 替换成 new_string，只改这一处，文件其余部分原样保留。\n" +
		"old_string 必须和文件内容逐字相同（含缩进），默认还必须在文件里唯一：不唯一时先扩大上下文重试，或按需设 replace_all 替换全部。\n" +
		"要先 read 一遍再改，照抄原文里真实存在的片段，不要凭记忆写。文件不存在时不会新建，改用 write。\n" +
		"改动先写临时文件再原子替换，不会留下半截文件；保留原文件的换行符风格（CRLF 或 LF）与权限。\n" +
		"要整份重写、新建文件或调整大量内容时用 write。"
}

func (t *Edit) Parameters() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "要修改的文件路径，可以是绝对路径，或相对工作目录的路径。"
    },
    "old_string": {
      "type": "string",
      "description": "要被替换的原片段，必须与文件内容逐字一致（含缩进和空行）。"
    },
    "new_string": {
      "type": "string",
      "description": "替换成的新内容，只有替换时存在的内容会被写入，其余部分不动。"
    },
    "replace_all": {
      "type": "boolean",
      "description": "为 true 时替换全部匹配；默认 false，要求 old_string 在文件里唯一。"
    }
  },
  "required": ["path", "old_string", "new_string"]
}`)
}

type editArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

func (t *Edit) Run(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var a editArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return tool.Result{}, fmt.Errorf("参数不是有效的 JSON 对象: %w", err)
		}
	}
	start := time.Now()
	if a.OldString == "" {
		return tool.Result{}, fmt.Errorf("参数 old_string 不能为空")
	}
	if a.OldString == a.NewString {
		return tool.Result{}, fmt.Errorf("old_string 与 new_string 相同，没有要改的内容")
	}
	abs, err := resolve(t.dir, a.Path)
	if err != nil {
		return tool.Result{}, err
	}
	info, err := checkFile(abs)
	if err != nil {
		return tool.Result{}, err
	}
	if info.Size() > MaxReadBytes {
		return tool.Result{}, fmt.Errorf("%s 有 %s，超过 edit 的 %s 上限，请用 shell 工具按需处理",
			abs, humanBytes(info.Size()), humanBytes(MaxReadBytes))
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return tool.Result{}, fmt.Errorf("读取 %s 失败: %w", abs, err)
	}
	if isBinary(data) {
		return tool.Result{}, fmt.Errorf("%s 看起来是二进制文件（含 NUL 字节），edit 只处理文本；请用 shell 工具处理", abs)
	}

	// 读出来的原文按 CRLF 统一成 LF 再匹配：模型从 read 的输出里抄片段时拿不到行尾的 \r，
	// 直接拿带 \r 的原文匹配会永远匹配不上。写回时再恢复原来的风格。
	crlf := strings.Contains(string(data), "\r\n")
	text := string(data)
	if crlf {
		text = strings.ReplaceAll(text, "\r\n", "\n")
	}
	old := a.OldString
	if crlf {
		old = strings.ReplaceAll(old, "\r\n", "\n")
	}
	// new_string 同样归一，避免替换后在同一文件里混出两种换行。
	repl := a.NewString
	if crlf {
		repl = strings.ReplaceAll(repl, "\r\n", "\n")
	}

	n := strings.Count(text, old)
	if n == 0 {
		return tool.Result{}, fmt.Errorf("%s 里找不到 old_string，文件没有被修改。先用 read 确认原文（注意缩进和空行要逐字一致）", abs)
	}
	if n > 1 && !a.ReplaceAll {
		return tool.Result{}, fmt.Errorf("%s 里 old_string 出现了 %d 处（第 %s 行），文件没有被修改：加上更多上下文让它唯一，或设 replace_all 全部替换",
			abs, n, lineList(text, old))
	}

	out := strings.ReplaceAll(text, old, repl) // replace_all 与 n==1 两种情况都适用
	if crlf {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	if err := writeAtomic(abs, out, info.Mode().Perm()); err != nil {
		return tool.Result{}, err
	}

	return tool.Result{
		Command: "edit " + abs,
		Stdout: fmt.Sprintf("已在 %s 替换 %d 处（第 %s 行）：%d 行，%d 字节",
			abs, n, lineList(text, old), len(splitLines(out)), len(out)),
		Duration: time.Since(start),
	}, nil
}

// lineList 列出 needle 在 text 里出现处的行号，最多列 5 处，多出来的写成“等 N 处”。
func lineList(text, needle string) string {
	var lines []string
	count := 0
	for i := 0; ; {
		j := strings.Index(text[i:], needle)
		if j < 0 {
			break
		}
		pos := i + j
		count++
		if len(lines) < 5 {
			lines = append(lines, fmt.Sprint(strings.Count(text[:pos], "\n")+1))
		}
		i = pos + len(needle)
	}
	if count > len(lines) {
		return strings.Join(lines, "、") + fmt.Sprintf(" 等 %d 处", count)
	}
	return strings.Join(lines, "、")
}
