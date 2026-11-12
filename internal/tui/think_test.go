package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shumengya/sprout/internal/agent/event"
)

func countThink(m *Model) int { return countKind(m, kindThink) }

func TestThinkingStreamsAsLivePreview(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 30)
	begin(m, "q")
	apply(m, event.ReasoningDelta{Text: "先看"}, event.ReasoningDelta{Text: "一下目录"})
	if countThink(m) != 1 || m.think == nil || !m.think.live {
		t.Fatal("增量应追加到同一个 live 思维链条目")
	}
	s := plain(m)
	mustContain(t, s, "思考中… 约5 token", "先看一下目录", "思考中")
}

func TestLiveThinkingOnlyPreviewsLastLines(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 40)
	begin(m, "q")
	var text strings.Builder
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&text, "第%02d行\n", i)
	}
	apply(m, event.ReasoningDelta{Text: text.String()})
	s := plain(m)
	mustContain(t, s, "第10行", "第09行", "第08行")
	mustNotContain(t, s, "第01行", "第07行")
}

func TestThinkingCollapsesWhenAnswerStarts(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 30)
	clk := withClock(m)
	begin(m, "q")
	apply(m, event.ReasoningDelta{Text: "内部推理过程内容"})
	clk.add(3 * time.Second)
	apply(m, event.MessageDelta{Text: "最终答案"})
	if m.think != nil || m.entries[len(m.entries)-2].live {
		t.Fatal("正文开始后思维链应结束流式")
	}
	// 折叠成一行：耗时，后面接着思考内容。token 数要等 MessageCompleted 带来内核的估算。
	mustContain(t, plain(m), "已思考 3秒：内部推理过程内容", "最终答案")
	apply(m, event.MessageCompleted{Content: "最终答案", Reasoning: "内部推理过程内容", ReasoningTokensEst: 6})
	mustContain(t, plain(m), "已思考 3秒 约6 token：内部推理过程内容")
}

func TestThinkToggleExpandsAndCollapses(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 40)
	begin(m, "q")
	apply(m, event.MessageCompleted{Content: "答", Reasoning: "第一行推理\n第二行推理", ReasoningTokensEst: 1234},
		event.TurnFinished{Reason: "stop"})
	mustContain(t, plain(m), "已思考 约1.2k token：第一行推理 第二行推理")

	submit(m, "/think")
	s := plain(m)
	mustContain(t, s, "已展开全部思考过程")
	// 展开后头部单独一行，全文按原来的换行缩进显示。
	mustContain(t, transcript(m), "已思考 约1.2k token\n  第一行推理\n  第二行推理")

	submit(m, "/think")
	s = plain(m)
	mustContain(t, s, "已折叠全部思考过程", "已思考 约1.2k token：第一行推理 第二行推理")
	mustNotContain(t, s, "  第二行推理")
}

func TestThinkingBeforeToolCallIsCollapsed(t *testing.T) {
	// 真实顺序：一次模型调用以 MessageCompleted 结束，随后才是 ToolRequested。
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 30)
	begin(m, "q")
	apply(m, event.ReasoningDelta{Text: "要先看目录"},
		event.MessageCompleted{Reasoning: "要先看目录", ToolCalls: []event.ToolCall{{ID: "c", Name: "bash"}}, ReasoningTokensEst: 5},
		requested("c", "bash", `{"command":"ls"}`))
	if m.think != nil || countThink(m) != 1 {
		t.Fatalf("工具调用前思维链应折叠且不重复: think=%v 数量=%d", m.think, countThink(m))
	}
	mustContain(t, plain(m), "已思考 约5 token：要先看目录", "bash")
}

func TestEachStepGetsItsOwnThinkingEntry(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 40)
	begin(m, "q")
	apply(m,
		event.ReasoningDelta{Text: "第一步想"},
		event.MessageCompleted{Reasoning: "第一步想", ToolCalls: []event.ToolCall{{ID: "c", Name: "bash"}}},
		requested("c", "bash", `{}`), event.ToolFinished{ID: "c", Name: "bash"},
		event.ReasoningDelta{Text: "第二步想"}, event.MessageDelta{Text: "答"},
		event.MessageCompleted{Content: "答", Reasoning: "第二步想"},
	)
	if countThink(m) != 2 {
		t.Fatalf("两次模型调用应各有一个思维链条目，得到 %d", countThink(m))
	}
}

func TestThinkingFromCompletedMessageWithoutDeltas(t *testing.T) {
	// 非流式服务：只在 MessageCompleted 里带完整思维链。
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 30)
	begin(m, "q")
	apply(m, event.MessageCompleted{Content: "答案", Reasoning: "事后给出的推理", ReasoningTokensEst: 7})
	if countThink(m) != 1 {
		t.Fatalf("应补一个思维链条目，得到 %d", countThink(m))
	}
	// 非流式没有思考开始的时刻，耗时未知，只显示 token 数。
	mustContain(t, plain(m), "已思考 约7 token：事后给出的推理", "答案")
}

func TestThinkingNotDuplicatedWhenStreamedAndCompleted(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 30)
	begin(m, "q")
	apply(m, event.ReasoningDelta{Text: "推理"}, event.MessageDelta{Text: "答"},
		event.MessageCompleted{Content: "答", Reasoning: "推理"})
	if countThink(m) != 1 {
		t.Fatalf("流式过的思维链不应在 MessageCompleted 时重复，得到 %d", countThink(m))
	}
}

func TestThinkingEndsWhenTurnIsCancelledMidThought(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 30)
	begin(m, "q")
	apply(m, event.ReasoningDelta{Text: "想到一半"}, event.TurnFinished{Reason: event.ReasonCancelled})
	if m.think != nil {
		t.Fatal("轮次结束后不应还有 live 思维链")
	}
	// 被取消时 MessageCompleted 不会来，没有内核的估算，也没有服务端的数字：不显示 token 数。
	var line string
	for _, l := range m.plainLines() {
		if strings.HasPrefix(l, "已思考") {
			line = l
		}
	}
	if !strings.HasSuffix(line, "：想到一半") || strings.Contains(line, "token") {
		t.Fatalf("思考条目 = %q", line)
	}
	mustBeIdle(t, m)
	mustNotContain(t, plain(m), "思考中…")
}

func TestCancelledThoughtDoesNotSuppressNextTurnsReasoning(t *testing.T) {
	// 上一轮思考到一半被取消，MessageCompleted 不会来；标志位必须在轮次结束时复位，
	// 否则下一轮非流式给出的思维链会被误判为“已经展示过”而丢失。
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 30)
	begin(m, "q1")
	apply(m, event.ReasoningDelta{Text: "想到一半"}, event.TurnFinished{Reason: event.ReasonCancelled})
	begin(m, "q2")
	apply(m, event.MessageCompleted{Content: "答", Reasoning: "第二轮的推理", ReasoningTokensEst: 6})
	mustContain(t, plain(m), "已思考 约6 token：第二轮的推理")
	if countThink(m) != 2 {
		t.Fatalf("两轮各应有一个思维链条目，得到 %d", countThink(m))
	}
}

func TestStatusLineShowsThinkingAndElapsed(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 30)
	begin(m, "q")
	apply(m, event.ReasoningDelta{Text: "想"})
	mustContain(t, plain(m), "思考中")
	apply(m, event.MessageDelta{Text: "答"})
	mustContain(t, plain(m), "生成中")

	// 超过几秒才显示耗时。begin() 没设置 started，这里按真实流程补上。
	m.started = time.Now().Add(-12 * time.Second)
	mustContain(t, plain(m), "生成中 12s")
}

func TestThinkTextIsSanitized(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 30)
	begin(m, "q")
	apply(m, event.ReasoningDelta{Text: "\x1b[31m红\x1b[0m\x07字"})
	if s := m.View().Content; strings.Contains(s, "\x1b[31m") || strings.Contains(s, "\x07") {
		t.Fatalf("思维链里的控制序列没有被清除: %q", s)
	}
}

func TestAssistantAnswerRendersMarkdown(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 40)
	begin(m, "q")
	apply(m, event.MessageDelta{Text: "## 结论\n- 要点一\n- **要点二**\n```\ncode\n```"})
	s := plain(m)
	mustContain(t, s, "结论", "• 要点一", "• 要点二", "  code")
	mustNotContain(t, s, "##", "**", "```")
}

func TestStreamingMarkdownToleratesHalfWrittenSyntax(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 40)
	begin(m, "q")
	for _, d := range []string{"这是 **粗", "体** 和 `co", "de`\n```py", "\nx = 1"} {
		apply(m, event.MessageDelta{Text: d})
		_ = m.View() // 每一步中间状态都不能 panic
	}
	mustContain(t, plain(m), "粗体", "code", "  x = 1")
}
