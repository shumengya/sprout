package agent

import (
	"sync"

	"github.com/shumengya/sprout/internal/agent/event"
)

// DefaultMaxParallelTools 是同一批 tool call 最多同时执行的个数。1 表示严格串行。
const DefaultMaxParallelTools = 4

// toolMsg 是 worker 发回主 goroutine 的消息。每个调用恰好发一条 started（被跳过的除外）和一条终态。
type toolMsg struct {
	idx     int
	started bool // true：开始执行；false：终态（fin/content 有效）
	fin     event.ToolFinished
	content string
	skipped bool // 终态里表示“根本没执行”（本轮在它开始前就被取消了）
}

// runTools 并发执行一批 tool call，把结果按调用顺序追加到历史。
// 只在本轮被取消时返回 error；此时没来得及执行的调用也会补上“未执行”结果，
// 保证每个 assistant tool call 在历史里都有对应的 tool 消息，否则下一次请求会被服务端拒绝。
//
// 并发模型：worker goroutine 只负责执行工具；所有事件的发送和对历史的写入都在调用者所在的
// agent goroutine 里完成。因此 event.Sink 不需要线程安全，也不会有历史上的数据竞争。
// 同一批调用之间互不保证顺序，模型应当只把互相独立的命令放在同一批里（系统提示词里有说明）。
func (t *turn) runTools(calls []ToolCall) error {
	limit := t.a.cfg.MaxParallelTools
	if limit <= 0 {
		limit = DefaultMaxParallelTools
	}
	// 每个调用最多发 2 条消息，缓冲区装得下，worker 永远不会因为主 goroutine 暂时没读而阻塞。
	msgs := make(chan toolMsg, 2*len(calls))

	go func() {
		sem := make(chan struct{}, limit)
		var wg sync.WaitGroup
		for i, call := range calls {
			// 在派发循环里按顺序占位：limit=1 时调用严格按顺序一个接一个开始，事件顺序也就稳定。
			select {
			case sem <- struct{}{}:
			case <-t.ctx.Done():
				msgs <- toolMsg{idx: i, skipped: true}
				continue
			}
			if t.ctx.Err() != nil { // 抢到名额的同时本轮被取消了
				<-sem
				msgs <- toolMsg{idx: i, skipped: true}
				continue
			}
			msgs <- toolMsg{idx: i, started: true}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				fin, content := t.execTool(call)
				msgs <- toolMsg{idx: i, fin: fin, content: content}
			}()
		}
		wg.Wait()
		close(msgs)
	}()

	results := make([]Message, len(calls))
	for m := range msgs {
		call := calls[m.idx]
		switch {
		case m.started:
			t.out.Emit(event.ToolRequested{Call: event.ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments}})
		case m.skipped:
			t.out.Emit(event.ToolFinished{ID: call.ID, Name: call.Name, Cancelled: true, Err: cancelledToolReply, Content: cancelledToolReply})
			results[m.idx] = toolMessage(call.ID, cancelledToolReply)
		default:
			m.fin.Content = m.content
			t.out.Emit(m.fin)
			results[m.idx] = toolMessage(call.ID, m.content)
		}
	}

	t.a.history = append(t.a.history, results...)
	if t.ctx.Err() != nil {
		return t.cancelled()
	}
	return nil
}
