// Package openai 实现 OpenAI Chat Completions 与 Responses 两种协议的 Provider。
// 两种协议的请求、响应、流式事件结构都不同，分别在 chat*.go 与 responses*.go 里独立实现，
// 只共用 HTTP 客户端、错误归一化和 SSE 读取。
package openai

import (
	"bufio"
	"io"
	"strings"
)

// sseEvent 是一个完整的 SSE 事件。
type sseEvent struct {
	Name string // event: 字段，Chat Completions 没有，Responses 有但类型也写在 data 的 type 里
	Data string // 多个 data: 行以 \n 连接
}

// sseReader 按 SSE 规范逐个读取事件。用 bufio.Reader 而不是 Scanner，
// 因为单行 data 可能很大（整段函数参数），Scanner 有单行长度上限。
type sseReader struct{ r *bufio.Reader }

func newSSEReader(r io.Reader) *sseReader {
	return &sseReader{r: bufio.NewReaderSize(r, 64*1024)}
}

// Next 返回下一个带 data 的事件；流正常结束时返回 io.EOF。
// 流在没有尾部空行时结束，已累积的 data 仍会作为最后一个事件返回，以兼容不规范的服务端。
func (s *sseReader) Next() (sseEvent, error) {
	var ev sseEvent
	var data []string
	for {
		line, err := s.r.ReadString('\n')
		if err != nil && line == "" {
			if err == io.EOF && len(data) > 0 {
				ev.Data = strings.Join(data, "\n")
				return ev, nil
			}
			return sseEvent{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" { // 空行：分派事件
			if len(data) > 0 {
				ev.Data = strings.Join(data, "\n")
				return ev, nil
			}
			ev = sseEvent{}
			continue
		}
		if line[0] == ':' { // 注释，常被服务端用作心跳
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			ev.Name = value
		case "data":
			data = append(data, value)
		}
	}
}
