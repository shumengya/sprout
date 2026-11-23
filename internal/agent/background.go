package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/shumengya/sprout/internal/agent/event"
	"github.com/shumengya/sprout/internal/agent/tool"
)

// 后台工具调用（见 tool.Backgroundable）。
//
// 后台任务属于发起它的那一轮：Run 要等它们全部结束才返回，取消这一轮或这一轮出错都会终止它们。
// 这样会话记录、事件顺序和“agent 只在空闲时被替换”的约定都不受影响；
// 用户在等待期间排队的输入照样会被接进来，所以等后台任务时仍然可以和模型对话。

// backgroundReply 是转入后台的调用立即回给模型的工具结果。
const backgroundReply = "已转入后台执行，任务编号 %s。任务结束后，结果会以一条“[后台任务已结束：…]”开头的消息自动送达，" +
	"不需要也无法主动查询；在此之前可以继续做别的事。如果眼下没有别的事可做，简短说明在等什么，然后结束这次回复。"

// backgroundParamSchema 是加进可后台工具参数里的开关。后台执行让模型多了“记着去处理迟到的结果”这件事，
// 前台更简单可靠，所以描述里要求默认不用，只在确有必要时才用。
const backgroundParamSchema = `{"type":"boolean","description":"是否转入后台执行，默认 false，一般不要用：前台执行更简单可靠。` +
	`只有这次调用明显要跑很久、而你在等它的同时确实有别的独立工作要做时才设为 true；` +
	`设为 true 时调用立即返回任务编号，执行结束后结果以一条消息自动送达。` +
	`下一步就要用到这次的结果、或者只是想同时做几件事（同一次回复里的多个调用本来就并行执行）时，都不要用。"}`

// queuedPoll 是等待后台任务期间查看用户排队输入的间隔。Input.Queued 只是一个函数，没有通知通道，只能轮询；
// 只在“模型已经给出答案、只剩后台任务”时才轮询，开销可以忽略。
const queuedPoll = 200 * time.Millisecond

// bgResult 是一个已经结束、还没送进历史的后台调用。
type bgResult struct {
	fin     event.ToolFinished
	content string
}

// background 管理一轮里转入后台的调用。worker goroutine 只往里放事件和结果，
// 转发事件、写历史都由 agent 主 goroutine 完成，所以 Sink 仍只在主 goroutine 里调用。
type background struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	// wake 在有新事件或新结果时放一个信号（容量 1，满了就不放）。等待方醒来后自己检查状态，漏掉信号不要紧。
	wake chan struct{}

	mu      sync.Mutex
	running int
	events  []event.Event // 后台调用经 tool.Call.Emit 交来的进度，等主 goroutine 转发
	done    []bgResult
}

func newBackground(parent context.Context) *background {
	ctx, cancel := context.WithCancel(parent)
	return &background{ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1)}
}

func (b *background) signal() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// emit 是后台调用的 tool.Call.Emit。它不能像前台那样阻塞等主 goroutine：主 goroutine 可能正在流式接收模型输出，
// 一等就是几十秒，后台任务会被白白卡住。
func (b *background) emit(e event.Event) {
	b.mu.Lock()
	b.events = append(b.events, e)
	b.mu.Unlock()
	b.signal()
}

// pending 报告还有没送进历史的后台调用（在跑的或已结束待送达的）。
func (b *background) pending() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.running > 0 || len(b.done) > 0
}

// flushEvents 转发排队的进度事件，只在主 goroutine 里调用。
func (b *background) flushEvents(out event.Sink) {
	b.mu.Lock()
	evs := b.events
	b.events = nil
	b.mu.Unlock()
	for _, e := range evs {
		out.Emit(e)
	}
}

// drain 一次取走排队的事件和已结束的结果。必须在同一把锁下取：一个调用的进度总在它的结果之前入队，
// 分两次取的话，它最后几条进度可能落在结果事件之后。
func (b *background) drain() ([]event.Event, []bgResult) {
	b.mu.Lock()
	defer b.mu.Unlock()
	evs, done := b.events, b.done
	b.events, b.done = nil, nil
	return evs, done
}

// abort 终止并等待全部后台调用，丢弃结果。只用于 panic 收尾这类历史本身要作废的场合。
func (b *background) abort() {
	b.cancel()
	b.wg.Wait()
}

// startBackground 在后台执行一次调用。由主 goroutine 在发出这次调用的 ToolRequested 之后调用，
// 这样它的进度事件不会跑到 tool.requested 前面。call 的参数已经去掉了 BackgroundParam。
func (t *turn) startBackground(call ToolCall) {
	b := t.bg
	b.mu.Lock()
	b.running++
	b.mu.Unlock()
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		fin, content := t.execTool(tool.WithCall(b.ctx, tool.Call{ID: call.ID, Emit: b.emit}), call)
		// 结果作为用户消息送达，不附图：目前能转入后台的工具（agent、shell、外部命令）都不产出图片。
		fin.Images = nil
		b.mu.Lock()
		b.running--
		b.done = append(b.done, bgResult{fin: fin, content: content})
		b.mu.Unlock()
		b.signal()
	}()
}

// deliverBackground 转发后台进度，并把已结束的后台调用作为用户消息接进历史，返回是否送达了结果。
// 只在安全点调用：一批工具结果之后、模型给出最终答案之后、一轮收尾时。这些时刻 tool 消息都已和 tool call 配齐，
// 接一条用户消息不破坏任何协议的顺序要求（与排队输入同理）。
func (t *turn) deliverBackground() bool {
	evs, done := t.bg.drain()
	for _, e := range evs {
		t.out.Emit(e)
	}
	for _, r := range done {
		r.fin.Content = r.content
		note := backgroundNote(r.fin)
		t.a.history = append(t.a.history, NewTextMessage(RoleUser, note))
		t.out.Emit(event.BackgroundFinished{Result: r.fin, Message: note})
	}
	return len(done) > 0
}

// awaitBackground 在模型给出最终答案、但还有后台调用没送达时等待：有结果送达或用户排了新输入就返回 nil，
// 由主循环接着请求模型；本轮被取消时按取消收尾。
func (t *turn) awaitBackground() error {
	var tick <-chan time.Time
	if t.queued != nil {
		tk := time.NewTicker(queuedPoll)
		defer tk.Stop()
		tick = tk.C
	}
	for {
		delivered := t.deliverBackground()
		if t.takeQueued() || delivered {
			return nil
		}
		select {
		case <-t.bg.wake:
		case <-tick:
		case <-t.ctx.Done():
			return t.cancelled()
		}
	}
}

// stopBackground 在一轮异常结束（取消、出错、超过步数）时终止还在跑的后台调用，等它们退出，
// 并把结果（通常是“被取消”）送进历史。必须在写中断说明之前调用：说明是 assistant 消息，
// 等待后台任务时历史末尾已经是模型的最终答案，先接上用户消息才不会出现两条连续的 assistant 消息。
func (t *turn) stopBackground() {
	if !t.bg.pending() {
		return
	}
	t.bg.cancel()
	t.bg.wg.Wait()
	t.deliverBackground()
}

// backgroundNote 是后台调用结束后写进历史的用户消息。方括号开头，与中断说明一样让模型认出这是系统补的说明。
func backgroundNote(fin event.ToolFinished) string {
	return fmt.Sprintf("[后台任务已结束：%s，任务编号 %s]\n\n%s", fin.Name, fin.ID, fin.Content)
}

// withBackgroundParam 在参数 schema 的 properties 里追加 BackgroundParam。
// properties 按原文拼接而不是解码成 map 再编码：map 会把参数按字母重排，而参数顺序会影响模型填写的顺序。
// schema 不是对象、或已经有同名参数时原样返回。
func withBackgroundParam(params json.RawMessage) json.RawMessage {
	var schema map[string]json.RawMessage
	if json.Unmarshal(params, &schema) != nil || schema == nil {
		return params
	}
	entry := `"` + tool.BackgroundParam + `":` + backgroundParamSchema
	props := bytes.TrimSpace(schema["properties"])
	if len(props) == 0 || string(props) == "null" {
		props = []byte("{" + entry + "}")
	} else {
		var existing map[string]json.RawMessage
		if json.Unmarshal(props, &existing) != nil {
			return params
		}
		if _, ok := existing[tool.BackgroundParam]; ok {
			return params
		}
		body := bytes.TrimSpace(props[1 : len(props)-1])
		sep := ","
		if len(body) == 0 {
			sep = ""
		}
		props = []byte("{" + string(body) + sep + entry + "}")
	}
	schema["properties"] = props
	out, err := json.Marshal(schema)
	if err != nil {
		return params
	}
	return out
}

// splitBackground 从调用参数里取出并去掉 BackgroundParam，返回交给工具的参数和是否要求后台执行。
// 参数不是 JSON 对象或没有这个开关时原样返回，参数错误由工具自己报告。
// 有的模型会把布尔值写成字符串，"true" 同样认。
func splitBackground(args string) (string, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(args), &m) != nil {
		return args, false
	}
	v, ok := m[tool.BackgroundParam]
	if !ok {
		return args, false
	}
	delete(m, tool.BackgroundParam)
	out, err := json.Marshal(m)
	if err != nil {
		return args, false
	}
	s := strings.TrimSpace(string(v))
	return string(out), s == "true" || s == `"true"`
}
