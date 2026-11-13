package file

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/shumengya/sprout/internal/agent/tool"
)

// Write 创建或覆盖一个文本文件。
type Write struct {
	dir string
}

func NewWrite(dir string) *Write { return &Write{dir: dir} }

func (t *Write) Name() string { return "write" }

func (t *Write) Description() string {
	return "创建或覆盖一个文本文件，内容完全由 content 决定（不会追加到原内容后面）。\n" +
		"相对路径相对启动时的工作目录；缺失的父目录会自动创建。\n" +
		"修改已有文件的局部内容时，先用 read 读出原文，改好后整份写回；需要追加、替换、拼接等复杂操作时改用 bash / powershell 工具。\n" +
		"目标已存在且是目录时失败；写入先写临时文件再替换，不会留下半截文件。"
}

func (t *Write) Parameters() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "要写入的文件路径，可以是绝对路径，或相对工作目录的路径。"
    },
    "content": {
      "type": "string",
      "description": "文件的完整内容，原样写入，不做换行或编码转换。"
    }
  },
  "required": ["path", "content"]
}`)
}

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (t *Write) Run(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var a writeArgs
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
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}

	// 已存在的目录一律拒绝，别让 os.Rename 把目录替换成文件。
	if info, statErr := os.Stat(abs); statErr == nil && info.IsDir() {
		return tool.Result{}, fmt.Errorf("%s: %w", abs, ErrIsDir)
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return tool.Result{}, fmt.Errorf("无法访问 %s: %w", abs, statErr)
	}

	existed := false
	if _, statErr := os.Stat(abs); statErr == nil {
		existed = true
	}

	if err := writeAtomic(abs, a.Content, fileMode(abs)); err != nil {
		return tool.Result{}, err
	}

	action := "新建"
	if existed {
		action = "覆盖"
	}
	lines := len(splitLines(a.Content))
	return tool.Result{
		Command:  "write " + abs,
		Stdout:   fmt.Sprintf("已%s %s：%d 行，%d 字节", action, abs, lines, len(a.Content)),
		Duration: time.Since(start),
	}, nil
}
