package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/event"
)

// eventMsg 携带一个内核事件。turn 用来丢弃上一轮遗留的消息。
type eventMsg struct {
	turn int
	ev   event.Event
}

// turnClosedMsg 表示这一轮的事件通道已关闭（agent goroutine 已退出）。
type turnClosedMsg struct{ turn int }

// chanSink 把内核事件写进 channel。quit 关闭后不再阻塞，避免 TUI 退出时 agent goroutine 卡在发送上。
type chanSink struct {
	ch   chan<- event.Event
	quit <-chan struct{}
}

func (s chanSink) Emit(e event.Event) {
	select {
	case s.ch <- e:
	case <-s.quit:
	}
}

// startTurn 在 Update 里调用：这里只准备状态，不做 IO。
// agent 的实际启动放在返回的 Cmd 里，由 Bubble Tea 在 Update 之外执行。
func (m *Model) startTurn(in agent.Input) tea.Cmd {
	m.turn++
	turn := m.turn
	m.started = m.now()
	m.busyStart()
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	events := make(chan event.Event, 64)
	m.events = events
	finished := make(chan struct{})
	m.finished = finished
	runner, quit := m.opts.Runner, m.quit

	start := func() tea.Msg {
		go func() {
			defer close(finished)
			defer close(events)
			sink := chanSink{events, quit}
			// 兜底：内核自己会 recover，这里防的是 Runner 的其他实现。
			defer func() {
				if r := recover(); r != nil {
					sink.Emit(event.Error{Message: fmt.Sprintf("内部错误: %v", r)})
					sink.Emit(event.TurnFinished{Reason: event.ReasonError})
				}
			}()
			_ = runner.Run(ctx, in, sink) // 失败已作为 Error 事件发出
		}()
		return nil
	}
	return tea.Batch(start, waitEvent(turn, events), m.sp.Tick)
}

// waitEvent 阻塞等待下一个事件。每处理完一个事件要重新挂起一次。
func waitEvent(turn int, ch <-chan event.Event) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return turnClosedMsg{turn}
		}
		return eventMsg{turn, e}
	}
}

// onEvent 把内核事件映射成 transcript 条目。
func (m *Model) onEvent(e event.Event) {
	// Usage 紧跟在同一次调用的 MessageCompleted 之后；中间隔了别的事件就不再属于那次调用。
	pending := m.usageThink
	m.usageThink = nil

	switch e := e.(type) {
	case event.ReasoningDelta:
		m.markGen()
		if m.think == nil {
			m.think = m.add(&entry{kind: kindThink, live: true, open: m.showThink, thinkStart: m.now()})
			m.callThink = m.think
			m.thinkShown = true
		}
		m.think.text += e.Text
		m.think.touch()
	case event.MessageDelta:
		m.markGen()
		m.endThink()
		if m.cur == nil {
			m.cur = m.add(&entry{kind: kindAssistant})
		}
		m.cur.text += e.Text
		m.cur.touch()
	case event.MessageCompleted:
		m.genEnd = m.now()
		m.endThink()
		// 没收到增量（非流式服务）时，用完整内容补上思维链和正文。
		// thinkShown 记录本次模型调用是否已经流式展示过思维链：MessageDelta 会提前结束 m.think，
		// 所以不能靠 m.think 是否为空来判断，否则流式过的思维链会在这里被重复补一次。
		if !m.thinkShown && e.Reasoning != "" {
			m.callThink = m.add(&entry{kind: kindThink, text: e.Reasoning, open: m.showThink})
		}
		if m.callThink != nil {
			m.callThink.thinkEst = e.ReasoningTokensEst
			m.callThink.touch()
		}
		m.thinkShown = false
		m.usageThink, m.callThink = m.callThink, nil
		if m.cur == nil && e.Content != "" {
			m.add(&entry{kind: kindAssistant, text: e.Content})
		}
		m.cur = nil
	case event.ToolRequested:
		m.endThink()
		m.cur = nil
		m.tools[e.Call.ID] = m.add(&entry{
			kind: kindTool, name: e.Call.Name, command: commandOf(e.Call.Arguments), running: true,
			skill: m.skillFor(e.Call.Arguments),
		})
	case event.ToolFinished:
		t := m.tools[e.ID]
		if t == nil { // 被取消而没来得及执行的调用没有 ToolRequested
			t = m.add(&entry{kind: kindTool, name: e.Name})
			m.tools[e.ID] = t
		}
		t.running, t.fin = false, e
		if t.command == "" {
			t.command = e.Command
		}
		t.touch()
	case event.Usage:
		m.addUsage(e)
		if pending != nil && e.ReasoningTokens > 0 {
			pending.thinkTokens = e.ReasoningTokens
			pending.touch()
		}
	case event.Compacted:
		m.add(&entry{kind: kindNotice, text: describeCompacted(e)})
		if e.Err == "" { // 失败时历史没动，上下文占用还是原来的
			m.ctxTokens = e.TokensAfter
			m.compactions++
		}
	case event.Retrying:
		m.add(&entry{kind: kindNotice, text: fmt.Sprintf("请求失败，%s后重试（%d/%d）：%s",
			fmtWait(e.Delay), e.Attempt, e.Max, e.Reason)})
	case event.Interrupted:
		m.endThink()
		// 正文已经流式显示过（Partial），这里只补一行说明，别把同一段话再打一遍。
		// 一个字都没收到时，把写进历史的那条消息显示出来，用户能看到模型答应了什么。
		if !e.Partial && e.Content != "" {
			m.interrupted = true
			m.add(&entry{kind: kindAssistant, text: e.Content})
		} else if note := interruptNote(e.Content); note != "" {
			m.interrupted = true
			m.add(&entry{kind: kindNotice, text: note})
		}
		m.cur = nil
	case event.Error:
		m.add(&entry{kind: kindError, text: e.Message})
	case event.TurnFinished:
		m.finishTurn(e.Reason)
	}
}

// addUsage 把一次模型调用的用量记进状态栏。
// 服务端报告的输入 token 含缓存读写，“输入”一项只算没命中缓存的部分，和“缓存”加起来才是全部输入。
// 上下文占用取最近一次请求的输入：它就是下一次请求大致要带的历史长度；服务端没报告时保持原值。
func (m *Model) addUsage(e event.Usage) {
	input := max(e.InputTokens-e.CacheReadTokens-e.CacheWriteTokens, 0)
	m.cost.add(m.opts.Info.Price, input, e.OutputTokens, e.CacheReadTokens, e.CacheWriteTokens)
	m.usage.Input += input
	m.usage.Output += e.OutputTokens
	m.usage.CacheRead += e.CacheReadTokens
	m.usage.CacheWrite += e.CacheWriteTokens
	if e.InputTokens > 0 {
		m.ctxTokens = e.InputTokens
	}
	m.updateSpeed(e.OutputTokens)
}

// markGen 记下本次模型调用第一次收到输出的时刻，输出速度和首字延迟从这里起算。
// 一次调用只记一次：多个增量先后到达时，首字是第一个。
func (m *Model) markGen() {
	if m.genStart.IsZero() {
		m.genStart = m.now()
		if !m.started.IsZero() {
			m.ttft = max(m.genStart.Sub(m.started), 0) // 时钟回拨时不写负数
		}
	}
}

// updateSpeed 用刚结束的这次调用的输出 token 数，除以从首个增量到消息完成的时间，得到输出速度。
// 没收到过增量（非流式服务）、时间为零或服务端没报告输出 token 时保持上一次的值，不编造数字。
func (m *Model) updateSpeed(output int) {
	start, end := m.genStart, m.genEnd
	m.genStart, m.genEnd = time.Time{}, time.Time{}
	if output <= 0 || start.IsZero() || end.IsZero() {
		return
	}
	if d := end.Sub(start).Seconds(); d > 0 {
		m.speed = float64(output) / d
	}
}

// resetSpeed 在新会话、恢复会话时清掉输出速度；重放历史事件时时钟几乎不走，算出来的速度没有意义。
func (m *Model) resetSpeed() {
	m.speed, m.ttft, m.compactions = 0, 0, 0
	m.genStart, m.genEnd = time.Time{}, time.Time{}
}

// endThink 结束当前的思维链条目：从“流式预览”变成一行摘要，并记下思考耗时。
func (m *Model) endThink() {
	if m.think == nil {
		return
	}
	if !m.think.thinkStart.IsZero() {
		m.think.thinkDur = m.now().Sub(m.think.thinkStart)
	}
	m.think.live = false
	m.think.touch()
	m.think = nil
}

func (m *Model) finishTurn(reason string) {
	// 一轮结束，“正在取消…”这类针对本轮的临时提示也该消失。
	m.busyStop()
	m.endThink()
	m.thinkShown = false
	m.callThink, m.usageThink = nil, nil
	m.genStart, m.genEnd = time.Time{}, time.Time{} // 中断或出错的调用没有 Usage，不能把起点留给下一次调用
	m.running, m.cur, m.status = false, nil, ""
	// 轮次异常结束时（通道提前关闭等）可能还有工具卡片停在“运行中…”，不能让它一直转下去。
	for _, e := range m.entries {
		if e.kind == kindTool && e.running {
			e.running, e.fin.Cancelled, e.fin.Err = false, true, "已取消"
			e.touch()
		}
	}
	if m.cancel != nil {
		m.cancel() // 释放 context
		m.cancel = nil
	}
	switch reason {
	case event.ReasonCancelled:
		// interrupted 事件已经说明了原因，不再重复一条“已取消本轮对话”。
		if !m.interrupted {
			m.add(&entry{kind: kindNotice, text: "已取消本轮对话。"})
		}
	case event.ReasonLength:
		m.add(&entry{kind: kindNotice, text: "模型输出被长度上限截断，回答可能不完整。"})
	}
	m.interrupted = false
}

// interruptNote 从写进历史的中断消息里取出方括号那句说明，用于单独显示。
// 消息形如「半截正文\n\n[对话中断：用户取消]」；没有说明时返回空串。
func interruptNote(content string) string {
	if i := strings.LastIndex(content, "\n\n[对话中断："); i >= 0 {
		return content[i+2:]
	}
	if strings.HasPrefix(content, "[对话中断：") {
		return content
	}
	return ""
}
