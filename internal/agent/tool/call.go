package tool

import (
	"context"

	"github.com/shumengya/sprout/internal/agent/event"
)

// Call 是内核在执行一次工具调用时放进 ctx 的调用信息。
//
// 目前只有 agent 工具用它：子代理的进度要在这次调用结束前就让界面看到，
// 而 Run 只在结束时返回一个 Result。
type Call struct {
	// ID 是这次 tool call 的 id，与 ToolRequested / ToolFinished 的 ID 相同。
	ID string
	// Emit 把一个事件交给内核，由内核在 agent 主 goroutine 里转发给 sink，所以并发调用也安全。
	// 只能在 Run 返回前调用：调用结束后内核不再接收。前台调用可能阻塞到主 goroutine 取走事件为止；
	// 转入后台的调用不阻塞，事件先排队，主 goroutine 在下一个空当转发。
	Emit func(event.Event)
}

type callKey struct{}

// WithCall 把调用信息放进 ctx。由内核在执行工具前调用。
func WithCall(ctx context.Context, c Call) context.Context {
	return context.WithValue(ctx, callKey{}, c)
}

// CallFrom 取出调用信息。不在内核里执行（比如测试直接调 Run）时 ok 为 false，Emit 为 nil。
func CallFrom(ctx context.Context) (Call, bool) {
	c, ok := ctx.Value(callKey{}).(Call)
	return c, ok
}
