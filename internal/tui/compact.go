package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/event"
)

// Compactor 让 TUI 手动压缩上下文（/compact），由 main 提供实现。*agent.Agent 的 Compact 满足它的语义，
// 但 main 要包一层：/model 切换会替换 agent，这里必须每次取当前的。
type Compactor interface {
	// Compact 把当前全部对话总结成摘要。focus 是用户对摘要的额外要求，可为空。
	// 它会阻塞到模型返回，所以 TUI 在 tea.Cmd 里调用，并用 ctx 支持 Ctrl+C 取消。
	// 过程中的事件（压缩结果、重试提示）发给 sink；没有对话可压时返回 agent.ErrNothingToCompact。
	Compact(ctx context.Context, focus string, sink event.Sink) error
}

// compactDoneMsg 是手动压缩结束后发回 Update 的消息。
type compactDoneMsg struct {
	events []event.Event
	err    error
}

// eventLog 收集事件。Compact 在同一个 goroutine 里同步发事件，返回之后才读，所以不需要加锁。
type eventLog struct{ events []event.Event }

func (l *eventLog) Emit(e event.Event) { l.events = append(l.events, e) }

// compactCmd 处理 /compact。arg 是用户给的额外要求（可为空）。
func (m *Model) compactCmd(arg string) tea.Cmd {
	switch {
	case m.opts.Compactor == nil:
		m.add(&entry{kind: kindNotice, text: "当前不能压缩上下文（没有可用的 agent）。"})
	case m.running || m.resuming || m.switching || m.compacting:
		m.status = "正在生成、恢复、切换模型或压缩中，暂时不能压缩上下文"
	default:
		return m.startCompact(arg)
	}
	return nil
}

// startCompact 在 Update 里调用：这里只准备状态，不做 IO，实际的模型调用放在返回的 Cmd 里。
// 压缩期间禁止发送和切换，避免新提问落进一个正在被改写的历史；Ctrl+C 可以取消。
func (m *Model) startCompact(focus string) tea.Cmd {
	m.compacting = true
	m.started = time.Now()
	m.status = ""
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	finished := make(chan struct{})
	m.finished = finished
	src := m.opts.Compactor

	run := func() (msg tea.Msg) {
		defer close(finished) // 放在 recover 之前：先写好 msg，再通知 Shutdown 可以退出了
		log := &eventLog{}
		defer func() {
			if r := recover(); r != nil {
				msg = compactDoneMsg{events: log.events, err: fmt.Errorf("内部错误: %v", r)}
			}
		}()
		err := src.Compact(ctx, strings.TrimSpace(focus), log)
		return compactDoneMsg{events: log.events, err: err}
	}
	return tea.Batch(run, m.sp.Tick)
}

// onCompactDone 处理压缩结果。压缩事件复用实时事件的同一套渲染（描述见 describeCompacted）；
// 没有事件的几种结局（没东西可压、被取消、其他错误）各给一句提示。
func (m *Model) onCompactDone(msg compactDoneMsg) {
	m.compacting, m.status = false, ""
	if m.cancel != nil {
		m.cancel() // 释放 context
		m.cancel = nil
	}
	shown := false
	for _, e := range msg.events {
		if _, ok := e.(event.Compacted); ok {
			shown = true
		}
		m.onEvent(e)
	}
	switch {
	case msg.err == nil:
	case errors.Is(msg.err, agent.ErrNothingToCompact):
		m.add(&entry{kind: kindNotice, text: "还没有可压缩的对话。"})
	case errors.Is(msg.err, context.Canceled):
		m.add(&entry{kind: kindNotice, text: "已取消压缩，对话保持原样。"})
	case !shown: // 摘要失败时内核已经发了带 Err 的压缩事件，那条提示就够了，别再重复一遍
		m.add(&entry{kind: kindError, text: "压缩上下文失败：" + msg.err.Error()})
	}
}
