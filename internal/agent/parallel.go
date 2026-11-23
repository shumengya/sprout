package agent

import (
	"fmt"
	"sync"

	"github.com/shumengya/sprout/internal/agent/event"
	"github.com/shumengya/sprout/internal/agent/tool"
)

// DefaultMaxParallelTools 是同一批 tool call 最多同时执行的个数。1 表示严格串行。
const DefaultMaxParallelTools = 4

// toolMsg 是 worker 发回主 goroutine 的消息。每个调用恰好发一条 started（被跳过的除外）和一条终态，
// 中间可能夹着若干条 progress（工具通过 tool.Call.Emit 转发的事件，如子代理的进度）。
// 转入后台的调用只有一条 started（background 为 true），由主 goroutine 收到后启动并当场给出占位结果。
type toolMsg struct {
	idx        int
	started    bool // true：开始执行；false：终态（fin/content 有效）
	background bool // started 时：这次调用转入后台
	fin        event.ToolFinished
	content    string
	skipped    bool        // 终态里表示“根本没执行”（本轮在它开始前就被取消了）
	progress   event.Event // 非 nil：工具执行中转发的事件，原样发给 sink
}

// plannedCall 是派发前对一次调用的安排。
type plannedCall struct {
	exec       ToolCall // 交给工具执行的调用：可后台工具的参数里去掉了 BackgroundParam
	slots      int      // 占几个并行名额
	background bool
}

// plan 决定每个调用怎么执行。未知工具按可并行处理（execTool 只会回错误）。
func (t *turn) plan(calls []ToolCall, limit int) []plannedCall {
	out := make([]plannedCall, len(calls))
	for i, call := range calls {
		p := plannedCall{exec: call, slots: 1}
		if tl, ok := t.lookup(call.Name); ok {
			if !tool.Parallel(tl) {
				p.slots = limit
			}
			if tool.Background(tl) {
				// 没要求后台时也去掉开关：它是内核加的参数，工具不认识它。
				p.exec.Arguments, p.background = splitBackground(call.Arguments)
			}
		}
		out[i] = p
	}
	return out
}

// runTools 并发执行一批 tool call，把结果按调用顺序追加到历史。
// 只在本轮被取消时返回 error；此时没来得及执行的调用也会补上“未执行”结果，
// 保证每个 assistant tool call 在历史里都有对应的 tool 消息，否则下一次请求会被服务端拒绝。
//
// 并发模型：worker goroutine 只负责执行工具；所有事件的发送和对历史的写入都在调用者所在的
// agent goroutine 里完成。因此 event.Sink 不需要线程安全，也不会有历史上的数据竞争。
// 同一批调用之间互不保证顺序，模型应当只把互相独立的命令放在同一批里（系统提示词里有说明）。
// 例外是声明了不可并行（tool.Parallelizable）的工具：它的调用独占执行，前后的调用都不与它重叠。
// 要求后台执行的调用当场以占位说明作为结果，真正的执行交给 t.bg，不占这一批的并行名额。
func (t *turn) runTools(calls []ToolCall) error {
	limit := t.a.cfg.MaxParallelTools
	if limit <= 0 {
		limit = DefaultMaxParallelTools
	}
	plan := t.plan(calls, limit)
	// 每个调用最多发 2 条状态消息，缓冲区装得下；progress 可能更多，worker 发它时会等主 goroutine 取走，
	// 主 goroutine 一直在读，所以只是短暂阻塞，不会死锁。
	msgs := make(chan toolMsg, 2*len(calls))

	go func() {
		sem := make(chan struct{}, limit)
		var wg sync.WaitGroup
		for i, p := range plan {
			// 在派发循环里按顺序占位：limit=1 时调用严格按顺序一个接一个开始，事件顺序也就稳定。
			// 声明不可并行的工具占满全部名额：要等前面的调用都结束才拿得齐，
			// 拿齐之后后面的调用也抢不到，于是它独占执行。
			if !t.acquire(sem, p.slots) {
				msgs <- toolMsg{idx: i, skipped: true}
				continue
			}
			if p.background {
				// 后台调用只在派发顺序里占一下位，让排在前面的独占调用照样把它挡在后面；启动它的是主 goroutine。
				<-sem
				msgs <- toolMsg{idx: i, started: true, background: true}
				continue
			}
			msgs <- toolMsg{idx: i, started: true}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() {
					for range p.slots {
						<-sem
					}
				}()
				fin, content := t.execTool(tool.WithCall(t.ctx, tool.Call{
					ID:   p.exec.ID,
					Emit: func(e event.Event) { msgs <- toolMsg{idx: i, progress: e} },
				}), p.exec)
				msgs <- toolMsg{idx: i, fin: fin, content: content}
			}()
		}
		wg.Wait()
		close(msgs)
	}()

	results := make([]Message, len(calls))
	for {
		var m toolMsg
		select {
		case msg, ok := <-msgs:
			if !ok {
				t.a.history = append(t.a.history, results...)
				if t.ctx.Err() != nil {
					return t.cancelled()
				}
				return nil
			}
			m = msg
		case <-t.bg.wake:
			// 之前转入后台的调用在这批执行期间的进度照常转发；它们的结果等这批结束后再送达。
			t.bg.flushEvents(t.out)
			continue
		}
		call := calls[m.idx]
		switch {
		case m.progress != nil:
			t.out.Emit(m.progress)
		case m.started:
			t.out.Emit(event.ToolRequested{Call: event.ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments}})
			if m.background {
				t.startBackground(plan[m.idx].exec)
				reply := fmt.Sprintf(backgroundReply, call.ID)
				t.out.Emit(event.ToolFinished{ID: call.ID, Name: call.Name, Content: reply, Background: true})
				results[m.idx] = toolMessage(call.ID, reply)
			}
		case m.skipped:
			t.out.Emit(event.ToolFinished{ID: call.ID, Name: call.Name, Cancelled: true, Err: cancelledToolReply, Content: cancelledToolReply})
			results[m.idx] = toolMessage(call.ID, cancelledToolReply)
		default:
			m.fin.Content = m.content
			t.out.Emit(m.fin)
			results[m.idx] = toolResultMessage(call.ID, m.content, m.fin.Images)
		}
	}
}

// acquire 按顺序占 n 个名额。本轮被取消时退还已占的名额并返回 false。
func (t *turn) acquire(sem chan struct{}, n int) bool {
	got := 0
	for got < n && t.ctx.Err() == nil {
		select {
		case sem <- struct{}{}:
			got++
		case <-t.ctx.Done():
		}
	}
	if t.ctx.Err() != nil { // 包括抢齐名额的同时本轮被取消
		for range got {
			<-sem
		}
		return false
	}
	return true
}
