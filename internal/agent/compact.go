package agent

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/shumengya/sprout/internal/agent/event"
)

const summarySystemPrompt = `你是对话摘要助手。把给出的对话压缩成一份摘要，供另一个 AI 助手接着工作。
必须保留：用户的目标和要求、已经完成的工作与结论、重要的文件路径 / 命令 / 数值 / 报错信息、尚未完成的事项和下一步。
省略寒暄和重复内容。用要点列表，不超过 800 字。如果输入里已有“此前的摘要”，把它和新对话合并成一份完整的新摘要。
只输出摘要本身，不要任何开场白。`

func (a *Agent) resetCalibration() { a.calibTokens, a.calibLen = 0, 0 }

// requestTokens 估算下一次请求的输入 token。
// 优先用上一次响应里服务端报告的真实用量，再加上此后新增消息的估算；没有真实用量时全部靠估算。
func (a *Agent) requestTokens() int {
	if a.calibLen > 0 && a.calibLen <= len(a.history) {
		return a.calibTokens + estimateMessages(a.history[a.calibLen:])
	}
	return estimateText(a.cfg.System) + estimateText(a.summary) + a.specTokens + estimateMessages(a.history)
}

// compact 在上下文接近窗口时压缩历史，返回历史是否被改动。force 表示服务端已经报告超长：
// 此时不再相信自己的估算，目标定为当前估算的一半。
//
// 先裁剪旧的工具输出（便宜，不丢对话），不够再把更早的对话总结成摘要。每一步都会发 Compacted 事件。
func (t *turn) compact(force bool) bool {
	a := t.a
	budget := contextBudget(a.cfg.ContextWindow, a.cfg.MaxOutputTokens)
	est := a.requestTokens()
	target := budget
	if force {
		target = est / 2
		if budget > 0 {
			target = min(target, budget)
		}
	}
	if target <= 0 || est <= target {
		return false
	}

	changed := false
	if n := PruneToolOutputs(a.history, keepRecentMsgs); n > 0 {
		a.resetCalibration()
		after := a.requestTokens()
		t.out.Emit(event.Compacted{Mode: event.CompactPrune, TokensBefore: est, TokensAfter: after, Dropped: n})
		changed, est = true, after
		if est <= target {
			return true
		}
	}
	if cut := a.cutPoint(target); cut > 0 {
		return t.summarizeAndCut(cut, est) || changed
	}
	return changed
}

// cutPoint 选择在哪条用户消息之前切开：切点之前的做摘要，之后的原样保留。
// 只在用户消息处切，所以不会拆开 assistant 的 tool_calls 和它对应的 tool 结果。
// 从最早的候选开始找第一个“保留部分装得进预算”的，尽量多留；都装不进就只保留最后一轮。返回 0 表示没法切。
func (a *Agent) cutPoint(target int) int {
	tailBudget := int(float64(target) * tailRatio)
	var users []int
	for i, m := range a.history {
		if i > 0 && m.Role == RoleUser {
			users = append(users, i)
		}
	}
	for _, i := range users {
		if estimateMessages(a.history[i:]) <= tailBudget {
			return i
		}
	}
	if len(users) == 0 {
		return 0
	}
	return users[len(users)-1]
}

func (t *turn) summarizeAndCut(cut, before int) bool {
	a := t.a
	text, err := t.summarize(a.history[:cut])
	if err != nil {
		if t.ctx.Err() != nil {
			return false // 被取消了，主循环会处理
		}
		// 摘要失败不能静默丢历史，也不该让用户这一轮白等：历史保持原样，提示一下，本轮继续。
		t.compactFailed = true
		t.out.Emit(event.Compacted{Mode: event.CompactSummary, TokensBefore: before, TokensAfter: before, Err: err.Error()})
		a.log.Warn("context summary failed", "error", err.Error())
		return false
	}
	a.summary = text
	a.history = slices.Clone(a.history[cut:])
	t.base = max(t.base-cut, 0) // 本轮开始前的历史长度随之缩短
	a.resetCalibration()
	t.out.Emit(event.Compacted{
		Mode: event.CompactSummary, TokensBefore: before, TokensAfter: a.requestTokens(), Dropped: cut, Summary: text,
	})
	return true
}

// summarize 调用同一个模型把 head（连同已有摘要）压缩成新摘要。不带工具，增量不外发，失败可退避重试。
func (t *turn) summarize(head []Message) (string, error) {
	a := t.a
	limit := max(a.cfg.ContextWindow/2, 4000)
	if a.cfg.ContextWindow <= 0 {
		limit = 60000
	}
	req := Request{
		Model: a.cfg.Model, System: summarySystemPrompt, MaxOutputTokens: a.cfg.MaxOutputTokens,
		Messages: []Message{{Role: RoleUser, Content: renderForSummary(a.summary, head, limit)}},
	}
	resp, err := t.retrying(func(func()) (Response, error) {
		return a.cfg.Provider.Stream(t.ctx, req, func(Chunk) error { return t.ctx.Err() })
	})
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(resp.Message.Content)
	switch {
	case resp.StopReason == StopLength:
		return "", errors.New("摘要被长度上限截断")
	case text == "":
		return "", errors.New("模型返回了空摘要")
	}
	return text, nil
}

// renderForSummary 把要摘要的消息渲染成纯文本。单条内容有长度上限；总量超过 limit 个字符时
// 保留最近的、丢弃更早的并注明，保证摘要请求本身不会超出窗口。
func renderForSummary(prev string, msgs []Message, limit int) string {
	lines := make([]string, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case RoleUser:
			lines = append(lines, "用户: "+clip(m.Content, 4000))
		case RoleAssistant:
			s := "助手: " + clip(m.Content, 4000)
			for _, c := range m.ToolCalls {
				s += fmt.Sprintf("\n  [调用 %s] %s", c.Name, clip(c.Arguments, 500))
			}
			lines = append(lines, s)
		case RoleTool:
			lines = append(lines, "  [结果] "+clip(m.Content, 1500))
		}
	}
	total, start := 0, len(lines)
	for i := len(lines) - 1; i >= 0; i-- {
		n := utf8.RuneCountInString(lines[i]) + 1
		if total+n > limit {
			break
		}
		total, start = total+n, i
	}
	var b strings.Builder
	if prev != "" {
		b.WriteString("## 此前的摘要\n" + prev + "\n\n")
	}
	b.WriteString("## 需要并入摘要的对话\n")
	if start > 0 {
		fmt.Fprintf(&b, "（更早的 %d 条消息因过长未纳入）\n", start)
	}
	b.WriteString(strings.Join(lines[start:], "\n"))
	return b.String()
}
