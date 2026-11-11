// Package transport 提供各 provider 适配器共用的 HTTP 与 SSE 基础设施：
// SSE 读取、默认 HTTP 客户端、错误归一化。
//
// 各协议的请求/响应结构差异很大，不在这里做兼容层；这里只有与协议无关的部分。
package transport

import (
	"bufio"
	"io"
	"strings"
)

// Event 是一个完整的 SSE 事件。
type Event struct {
	Name string // event: 字段；只有部分协议使用
	Data string // 多个 data: 行以 \n 连接
}

// Reader 按 SSE 规范逐个读取事件。用 bufio.Reader 而不是 Scanner，
// 因为单行 data 可能很大（整段函数参数），Scanner 有单行长度上限。
type Reader struct{ r *bufio.Reader }

func NewReader(r io.Reader) *Reader {
	return &Reader{r: bufio.NewReaderSize(r, 64*1024)}
}

// Next 返回下一个带 data 的事件；流正常结束时返回 io.EOF。
// 流在没有尾部空行时结束，已累积的 data 仍会作为最后一个事件返回，以兼容不规范的服务端。
func (s *Reader) Next() (Event, error) {
	var ev Event
	var data []string
	for {
		line, err := s.r.ReadString('\n')
		if err != nil && line == "" {
			if err == io.EOF && len(data) > 0 {
				ev.Data = strings.Join(data, "\n")
				return ev, nil
			}
			return Event{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" { // 空行：分派事件
			if len(data) > 0 {
				ev.Data = strings.Join(data, "\n")
				return ev, nil
			}
			ev = Event{}
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
