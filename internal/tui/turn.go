package tui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

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
func (m *Model) startTurn(prompt string) tea.Cmd {
	m.turn++
	turn := m.turn
	m.started = time.Now()
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
			_ = runner.Run(ctx, prompt, sink) // 失败已作为 Error 事件发出
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
	switch e := e.(type) {
	case event.ReasoningDelta:
		if m.think == nil {
			m.think = m.add(&entry{kind: kindThink, live: true})
			m.thinkShown = true
		}
		m.think.text += e.Text
		m.think.touch()
	case event.MessageDelta:
		m.endThink()
		if m.cur == nil {
			m.cur = m.add(&entry{kind: kindAssistant})
		}
		m.cur.text += e.Text
		m.cur.touch()
	case event.MessageCompleted:
		m.endThink()
		// 没收到增量（非流式服务）时，用完整内容补上思维链和正文。
		// thinkShown 记录本次模型调用是否已经流式展示过思维链：MessageDelta 会提前结束 m.think，
		// 所以不能靠 m.think 是否为空来判断，否则流式过的思维链会在这里被重复补一次。
		if !m.thinkShown && e.Reasoning != "" {
			m.add(&entry{kind: kindThink, text: e.Reasoning})
		}
		m.thinkShown = false
		if m.cur == nil && e.Content != "" {
			m.add(&entry{kind: kindAssistant, text: e.Content})
		}
		m.cur = nil
	case event.ToolRequested:
		m.endThink()
		m.cur = nil
		m.tools[e.Call.ID] = m.add(&entry{
			kind: kindTool, name: e.Call.Name, command: commandOf(e.Call.Arguments), running: true,
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
		m.tokens += e.TotalTokens
	case event.Retrying:
		m.add(&entry{kind: kindNotice, text: fmt.Sprintf("请求失败，%s后重试（%d/%d）：%s",
			fmtWait(e.Delay), e.Attempt, e.Max, e.Reason)})
	case event.Error:
		m.add(&entry{kind: kindError, text: e.Message})
	case event.TurnFinished:
		m.finishTurn(e.Reason)
	}
}

// endThink 结束当前的思维链条目：从“流式预览”变成可折叠的摘要。
func (m *Model) endThink() {
	if m.think == nil {
		return
	}
	m.think.live = false
	m.think.touch()
	m.think = nil
}

func (m *Model) finishTurn(reason string) {
	// 一轮结束，“正在取消…”这类针对本轮的临时提示也该消失。
	m.endThink()
	m.thinkShown = false
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
		m.add(&entry{kind: kindNotice, text: "已取消本轮对话。"})
	case event.ReasonLength:
		m.add(&entry{kind: kindNotice, text: "模型输出被长度上限截断，回答可能不完整。"})
	}
}
