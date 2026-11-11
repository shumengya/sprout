// Package openai 实现 OpenAI Chat Completions 与 Responses 两种协议的 Provider。
// 两种协议的请求、响应、流式事件结构都不同，分别在 chat*.go 与 responses*.go 里独立实现，
// 只共用 HTTP 客户端、错误归一化和 SSE 读取（见 internal/provider/transport）。
package openai

import (
	"io"

	"github.com/shumengya/sprout/internal/provider/transport"
)

// sseEvent 是一个完整的 SSE 事件，保留本包内的短名字。
type sseEvent = transport.Event

// sseReader 是 transport.Reader 的类型别名，只是为了本包代码读起来更短。
type sseReader = transport.Reader

func newSSEReader(r io.Reader) *sseReader { return transport.NewReader(r) }
