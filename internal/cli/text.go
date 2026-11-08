package cli

import (
	"encoding/json"
	"strings"
)

// commandOf 从工具参数里取 command 用于展示，取不到时退回原文。
// CLI 不依赖具体工具的 schema，这只是个展示用的启发式。
func commandOf(args string) string {
	var a struct {
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(args), &a) == nil && strings.TrimSpace(a.Command) != "" {
		return a.Command
	}
	return args
}

// clean 去掉会操控终端的控制字符（含 ESC，转义序列因此失效）。保留换行和制表符。
// 模型输出和命令内容都可能来自不可信的网页或文件，不能原样写到终端上。
// 按单个字符处理，所以流式增量把转义序列切成两半也同样安全。
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r < 0x20, r == 0x7f, r >= 0x80 && r < 0xa0:
			return -1
		}
		return r
	}, s)
}

func firstLine(s string) string {
	line, rest, multi := strings.Cut(s, "\n")
	if multi && strings.TrimSpace(rest) != "" {
		return line + " …"
	}
	return line
}
