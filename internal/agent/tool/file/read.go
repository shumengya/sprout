package file

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shumengya/sprout/internal/agent/tool"
)

const (
	// DefaultReadLines 是一次 read 默认返回的行数。
	DefaultReadLines = 2000
	// maxReadLines 是 limit 的上限，防止一次把整个大日志塞进上下文。
	maxReadLines = 20000
	// maxReadOutput 是返回给模型的字节上限，与 shell 工具一致。
	maxReadOutput = 50 * 1024
)

// Read 按行读取文本文件。
type Read struct {
	dir string
}

func NewRead(dir string) *Read { return &Read{dir: dir} }

func (t *Read) Name() string { return "read" }

func (t *Read) Description() string {
	return "读取一个文本文件，按行号返回。适合看代码、配置和日志，不必起 shell 进程。\n" +
		"相对路径相对启动时的工作目录。文件超过 10MB 或看起来是二进制（含 NUL 字节）时拒绝读取，请改用 bash / powershell 工具。\n" +
		"默认从第 1 行读 2000 行；文件更长时用 offset / limit 读后续部分。输出超过 50KB 会被截断。"
}

func (t *Read) Parameters() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "要读取的文件路径，可以是绝对路径，或相对工作目录的路径。"
    },
    "offset": {
      "type": "integer",
      "minimum": 1,
      "description": "从第几行开始读（1 起始），默认 1。"
    },
    "limit": {
      "type": "integer",
      "minimum": 1,
      "maximum": 20000,
      "description": "最多读多少行，默认 2000，最大 20000。"
    }
  },
  "required": ["path"]
}`)
}

// readArgs 是 read 的参数。
type readArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

func (t *Read) Run(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var a readArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return tool.Result{}, fmt.Errorf("参数不是有效的 JSON 对象: %w", err)
		}
	}
	start := time.Now()
	abs, err := resolve(t.dir, a.Path)
	if err != nil {
		return tool.Result{}, err
	}
	info, err := checkFile(abs)
	if err != nil {
		return tool.Result{}, err
	}
	if info.Size() > MaxReadBytes {
		return tool.Result{}, fmt.Errorf("%s 有 %s，超过 read 的 %s 上限，请用 bash / powershell 工具读取指定部分",
			abs, humanBytes(info.Size()), humanBytes(MaxReadBytes))
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return tool.Result{}, fmt.Errorf("读取 %s 失败: %w", abs, err)
	}
	if isBinary(data) {
		return tool.Result{}, fmt.Errorf("%s 看起来是二进制文件（含 NUL 字节），read 只处理文本；请用 shell 工具处理", abs)
	}

	all := splitLines(string(data))
	offset := max(a.Offset, 1)
	if offset > len(all) && len(all) > 0 {
		return tool.Result{}, fmt.Errorf("%s 只有 %d 行，offset %d 超出范围", abs, len(all), offset)
	}
	limit := a.Limit
	if limit <= 0 {
		limit = DefaultReadLines
	}
	limit = min(limit, maxReadLines)

	// 从 offset 开始取，直到行数或字节数任何一个先到上限。
	var b strings.Builder
	width := len(strconv.Itoa(len(all)))
	last, truncated := offset-1, false
	for i := offset - 1; i < len(all) && i < offset-1+limit; i++ {
		line := fmt.Sprintf("%*d\t%s\n", width, i+1, all[i])
		if b.Len()+len(line) > maxReadOutput {
			truncated = true
			break
		}
		b.WriteString(line)
		last = i
	}
	if offset-1 < len(all) && last < offset-1+limit-1 && len(all) > last+1 {
		truncated = true
	}

	var note strings.Builder
	fmt.Fprintf(&note, "%s：共 %d 行，本次返回第 %d–%d 行", abs, len(all), offset, max(last+1, offset))
	if truncated {
		fmt.Fprintf(&note, "（输出达到上限被截断，用 offset=%d 继续读）", last+2)
	}
	return tool.Result{
		Command:  "read " + abs,
		Stdout:   note.String() + "\n" + b.String(),
		Duration: time.Since(start),
	}, nil
}

// splitLines 按 \n 切行，兼容 CRLF；文件末尾的换行不产生空行。
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimSuffix(ln, "\r")
	}
	return lines
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%dB", n)
}
