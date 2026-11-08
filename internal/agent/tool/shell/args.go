package shell

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Args 是 bash / powershell 工具共用的参数。
type Args struct {
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// ParseArgs 解析并校验工具参数。timeout_seconds 缺省或越界时取默认值/上限，不报错。
func ParseArgs(raw json.RawMessage) (Args, time.Duration, error) {
	var a Args
	if len(raw) == 0 {
		return a, 0, errors.New("缺少参数，需要 {\"command\": \"...\"}")
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return a, 0, fmt.Errorf("参数不是有效的 JSON 对象: %w", err)
	}
	if strings.TrimSpace(a.Command) == "" {
		return a, 0, errors.New("参数 command 不能为空")
	}
	timeout := DefaultTimeout
	if a.TimeoutSeconds > 0 {
		timeout = min(time.Duration(a.TimeoutSeconds)*time.Second, MaxTimeout)
	}
	return a, timeout, nil
}

// Schema 是两个工具共用的参数 JSON Schema。
var Schema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": {
      "type": "string",
      "description": "要执行的完整命令或脚本。"
    },
    "timeout_seconds": {
      "type": "integer",
      "minimum": 1,
      "maximum": 600,
      "description": "超时秒数，默认 120，最大 600。"
    }
  },
  "required": ["command"]
}`)
