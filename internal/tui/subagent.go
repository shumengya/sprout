package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/shumengya/sprout/internal/agent/event"
)

// 子代理运行中，卡片下面最多列出最近几次工具调用；展开后列出全部。
const subPreview = 4

// subRun 是 agent 工具卡片上的子代理进度，由 event.Subagent 逐条累积。
// 恢复会话时没有这些事件（子代理的过程在单独的记录文件里），卡片只显示最后的报告。
type subRun struct {
	agent  string
	steps  []subStep
	byID   map[string]int // 子代理的 tool call id → steps 下标
	calls  int            // 模型调用次数
	output int            // 输出 token 累计
	errMsg string
}

type subStep struct {
	name    string
	command string
	running bool
	fin     event.ToolFinished
}

// onSubagent 处理子代理转发上来的事件：用量计入花费，工具调用挂到对应的 agent 卡片上。
func (m *Model) onSubagent(e event.Subagent) {
	if u, ok := e.Event.(event.Usage); ok {
		m.countUsage(u)
	}
	t := m.tools[e.CallID]
	if t == nil { // 恢复会话时用量记录排在工具卡片之前，只记花费
		return
	}
	if t.sub == nil {
		t.sub = &subRun{agent: e.Agent, byID: map[string]int{}}
	}
	s := t.sub
	switch in := e.Event.(type) {
	case event.ToolRequested:
		s.byID[in.Call.ID] = len(s.steps)
		s.steps = append(s.steps, subStep{name: in.Call.Name, command: commandOf(in.Call.Arguments), running: true})
	case event.ToolFinished:
		i, ok := s.byID[in.ID]
		if !ok { // 没来得及执行就被取消的调用没有 ToolRequested
			i = len(s.steps)
			s.byID[in.ID] = i
			s.steps = append(s.steps, subStep{name: in.Name, command: in.Command})
		}
		s.steps[i].running, s.steps[i].fin = false, in
	case event.Usage:
		s.calls++
		s.output += in.OutputTokens
	case event.Error:
		s.errMsg = in.Message
	default:
		return
	}
	t.touch()
}

// drawSub 画子代理的进度：一行汇总，再列出工具调用（运行中或收起时只列最近几次）。
func (e *entry) drawSub(w int) string {
	s := e.sub
	var b strings.Builder
	head := fmt.Sprintf("子代理 %s · %d 次工具调用", s.agent, len(s.steps))
	if s.output > 0 {
		head += " · 输出 " + fmtTokens(s.output) + " token"
	}
	b.WriteString("\n    " + styFaint.Render(ansi.Truncate(sanitize(head), max(w-4, 1), "…")))

	steps := s.steps
	if !e.open && len(steps) > subPreview {
		b.WriteString("\n    " + styFaint.Render(fmt.Sprintf("… 前面还有 %d 次", len(steps)-subPreview)))
		steps = steps[len(steps)-subPreview:]
	}
	for _, st := range steps {
		mark := styFaint.Render("✓ ")
		switch {
		case st.running:
			mark = styWarn.Render("▸ ")
		case st.fin.Cancelled:
			mark = styWarn.Render("- ")
		case st.fin.Err != "" || st.fin.ExitCode != 0 || st.fin.TimedOut:
			mark = styErr.Render("✗ ")
		}
		line := st.name + "(" + strings.Join(strings.Fields(sanitize(st.command)), " ") + ")"
		b.WriteString("\n    " + mark + styFaint.Render(ansi.Truncate(line, max(w-6, 1), "…")))
	}
	if s.errMsg != "" {
		msg, _, _ := strings.Cut(sanitize(s.errMsg), "\n")
		b.WriteString("\n    " + styErr.Render(ansi.Truncate("子代理出错："+msg, max(w-4, 1), "…")))
	}
	return b.String()
}
