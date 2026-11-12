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
省略寒暄和重复内容。用要点列表，不超过 800 字。
只输出摘要本身，不要任何开场白。`

// mergeSummarySystemPrompt 用于把两层相邻的旧摘要合并成一层。
const mergeSummarySystemPrompt = `你是对话摘要助手。下面按时间顺序给出两段对话摘要，较早的在前。把它们合并成一份更精简的摘要，供另一个 AI 助手接着工作。
必须保留：用户的目标和要求、已经完成的工作与结论、重要的文件路径 / 命令 / 数值 / 报错信息、尚未完成的事项和下一步。
两段内容有冲突时以较晚的为准；已经被后来推翻或完成的细节可以省略。用要点列表，不超过 800 字。
只输出合并后的摘要本身，不要任何开场白。`

// maxSummaryLayers 是摘要最多保留的层数。每次压缩只把新切掉的那段总结成新的一层追加在后面，
// 不重新消化旧摘要；超过这个层数时把最旧的两层合并成一层。最近的几段因此保持详细，越旧的被压得越狠，
// 而不是每次都把整份摘要重写一遍、一层层失真。
const maxSummaryLayers = 3

// summaryLayerSep 分隔摘要的各层。摘要对外始终是一个字符串（会话记录和 Restore 都按字符串存取），
// 层次靠这段分隔文字表达，它同时告诉模型后面的内容更晚。没有它的摘要（旧会话）就是单层。
const summaryLayerSep = "\n\n（以上是较早的对话摘要，以下是之后的对话摘要）\n\n"

// splitSummary 把摘要拆成各层，从旧到新。
func splitSummary(s string) []string {
	var layers []string
	for _, p := range strings.Split(s, summaryLayerSep) {
		if p = strings.TrimSpace(p); p != "" {
			layers = append(layers, p)
		}
	}
	return layers
}

func joinSummary(layers []string) string { return strings.Join(layers, summaryLayerSep) }

// minSummaryOutput 是摘要请求的输出上限下限。思考模式模型的推理 token 也计入输出上限，
// 用户为对话设置的 maxOutput 偏小时（真实测试里 600 就被截断了），摘要会在写完前被截断而失败。
const minSummaryOutput = 4000

// summaryMaxOutput 返回摘要请求的输出上限：未设置（0）时保持不发该参数，已设置则至少 minSummaryOutput。
func summaryMaxOutput(configured int) int {
	if configured <= 0 {
		return 0
	}
	return max(configured, minSummaryOutput)
}

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
	// 先裁剪旧工具输出（便宜，不丢对话）。从最旧的开始，估算降到目标的 pruneHeadroom 以下就停，
	// 不一次裁光：能多保留一些旧输出的原文，也让服务端的前缀缓存少失效一些。
	goal := int(float64(target) * pruneHeadroom)
	if n, _, savedCalibrated := pruneToward(a.history, keepRecentMsgs, est-goal, a.calibLen); n > 0 {
		// 校准值是服务端对 history[:calibLen] 报告的真实用量，被裁掉的那部分要从中扣掉；
		// 直接复位会退回纯估算，而纯估算整体偏高，会让紧接着的下一步又判定超限。
		if a.calibLen > 0 {
			if a.calibTokens > savedCalibrated {
				a.calibTokens -= savedCalibrated
			} else {
				a.resetCalibration()
			}
		}
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
	text, err := t.foldSummary(a.history[:cut])
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
	DropNative(a.history)       // 保留下来的轮次前面换成了摘要，它们的思考块已对不上前缀
	t.base = max(t.base-cut, 0) // 本轮开始前的历史长度随之缩短
	a.resetCalibration()
	t.out.Emit(event.Compacted{
		Mode: event.CompactSummary, TokensBefore: before, TokensAfter: a.requestTokens(), Dropped: cut, Summary: text,
	})
	return true
}

// foldSummary 把 head 总结成新的一层，追加到现有摘要后面，返回完整的新摘要。
// 超过 maxSummaryLayers 层时再把最旧的两层合并。合并失败不算压缩失败：新的一层已经有了，
// 只是摘要暂时多一层，下次压缩再试；但被取消时整体放弃，保持历史和摘要原样。
func (t *turn) foldSummary(head []Message) (string, error) {
	layer, err := t.summarize(head)
	if err != nil {
		return "", err
	}
	layers := append(splitSummary(t.a.summary), layer)
	if len(layers) > maxSummaryLayers {
		merged, err := t.mergeSummaries(layers[0], layers[1])
		switch {
		case err == nil:
			layers = append([]string{merged}, layers[2:]...)
		case t.ctx.Err() != nil:
			return "", err
		default:
			t.a.log.Warn("summary merge failed", "error", err.Error())
		}
	}
	return joinSummary(layers), nil
}

// summarize 调用同一个模型把 head 压缩成一层摘要。不带工具，增量不外发，失败可退避重试。
// 只总结 head 本身，旧摘要不参与：旧的各层原样保留，不会被重写。
func (t *turn) summarize(head []Message) (string, error) {
	a := t.a
	limit := max(a.cfg.ContextWindow/2, 4000)
	if a.cfg.ContextWindow <= 0 {
		limit = 60000
	}
	return t.askSummary(summarySystemPrompt, renderForSummary(head, limit))
}

// mergeSummaries 把相邻的两层旧摘要（older 在前）合并成一层。
func (t *turn) mergeSummaries(older, newer string) (string, error) {
	return t.askSummary(mergeSummarySystemPrompt, "## 较早的摘要\n"+older+"\n\n## 较晚的摘要\n"+newer)
}

// askSummary 发一次不带工具的摘要请求。被长度截断或返回空文本都算失败：半截摘要会让后续对话丢信息。
func (t *turn) askSummary(system, input string) (string, error) {
	a := t.a
	req := Request{
		Model: a.cfg.Model, System: system, MaxOutputTokens: summaryMaxOutput(a.cfg.MaxOutputTokens),
		Messages: []Message{NewTextMessage(RoleUser, input)},
	}
	resp, err := t.retrying(func(func()) (Response, error) {
		return a.cfg.Provider.Stream(t.ctx, req, func(Chunk) error { return t.ctx.Err() })
	})
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(resp.Message.Text())
	switch {
	case t.ctx.Err() != nil:
		return "", t.ctx.Err()
	case resp.StopReason == StopLength:
		return "", errors.New("摘要被长度上限截断")
	case text == "":
		return "", errors.New("模型返回了空摘要")
	}
	return text, nil
}

// renderForSummary 把要摘要的消息渲染成纯文本。单条内容有长度上限；总量超过 limit 个字符时
// 保留最近的、丢弃更早的并注明，保证摘要请求本身不会超出窗口。
func renderForSummary(msgs []Message, limit int) string {
	lines := make([]string, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case RoleUser:
			lines = append(lines, "用户: "+clip(m.Text(), 4000))
		case RoleAssistant:
			s := "助手: " + clip(m.Text(), 4000)
			for _, c := range m.ToolCalls {
				s += fmt.Sprintf("\n  [调用 %s] %s", c.Name, clip(c.Arguments, 500))
			}
			lines = append(lines, s)
		case RoleTool:
			lines = append(lines, "  [结果] "+clip(m.Text(), 1500))
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
	b.WriteString("## 需要并入摘要的对话\n")
	if start > 0 {
		fmt.Fprintf(&b, "（更早的 %d 条消息因过长未纳入）\n", start)
	}
	b.WriteString(strings.Join(lines[start:], "\n"))
	return b.String()
}
