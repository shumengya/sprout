package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent/event"
	"github.com/shumengya/sprout/internal/agent/tool"
)

func isSummaryReq(r Request) bool { return r.System == summarySystemPrompt }

// textTurns 生成 n 轮纯文本对话（每轮 user + assistant），每条约 size 个汉字。
func textTurns(n, size int) []Message {
	var h []Message
	for i := 0; i < n; i++ {
		h = append(h,
			Message{Role: RoleUser, Content: strings.Repeat("问", size)},
			Message{Role: RoleAssistant, Content: strings.Repeat("答", size)})
	}
	return h
}

// compactAgent 构造窗口很小的 agent，并塞入历史。window=800 → 预算约 540 token。
func compactAgent(t *testing.T, p Provider, window int, history []Message) *Agent {
	t.Helper()
	a := newAgentCfg(t, Config{Provider: p, ContextWindow: window, Retry: &RetryPolicy{}})
	a.Restore(history, "")
	return a
}

func compactedEvents(c *collector) []event.Compacted {
	var out []event.Compacted
	for _, e := range c.events {
		if ce, ok := e.(event.Compacted); ok {
			out = append(out, ce)
		}
	}
	return out
}

func TestProactiveSummaryCompaction(t *testing.T) {
	p := &scripted{steps: []scriptStep{say("摘要：用户在聊很多事"), say("最终回答")}}
	a := compactAgent(t, p, 800, textTurns(8, 100))
	var c collector

	if err := a.Run(context.Background(), "新问题", &c); err != nil {
		t.Fatal(err)
	}
	if len(p.reqs) != 2 || !isSummaryReq(p.reqs[0]) || isSummaryReq(p.reqs[1]) {
		t.Fatalf("应先发摘要请求再发正式请求: %d 次", len(p.reqs))
	}
	sum := p.reqs[0]
	if len(sum.Tools) != 0 || len(sum.Messages) != 1 || !strings.Contains(sum.Messages[0].Content, "问问问") {
		t.Errorf("摘要请求不应带工具，且内容里要有被压缩的对话: %+v", sum)
	}

	main := p.reqs[1]
	if !strings.Contains(main.System, "之前对话的摘要") || !strings.Contains(main.System, "用户在聊很多事") {
		t.Errorf("正式请求的系统提示词应带摘要: %.200s", main.System)
	}
	if main.Messages[0].Role != RoleUser || len(main.Messages) >= 17 {
		t.Errorf("历史应从用户消息开始且变短: %d 条", len(main.Messages))
	}
	last := main.Messages[len(main.Messages)-1]
	if last.Role != RoleUser || last.Content != "新问题" {
		t.Errorf("本轮的用户消息必须保留: %+v", last)
	}

	evs := compactedEvents(&c)
	if len(evs) != 1 || evs[0].Mode != event.CompactSummary || evs[0].Dropped == 0 || evs[0].Err != "" ||
		evs[0].Summary != "摘要：用户在聊很多事" || evs[0].TokensAfter >= evs[0].TokensBefore {
		t.Fatalf("Compacted 事件 = %+v", evs)
	}
	if a.Summary() != "摘要：用户在聊很多事" {
		t.Errorf("Summary() = %q", a.Summary())
	}
}

func TestNoProactiveCompactionWithoutWindow(t *testing.T) {
	p := &scripted{steps: []scriptStep{say("好")}}
	a := compactAgent(t, p, 0, textTurns(30, 200))
	var c collector
	if err := a.Run(context.Background(), "q", &c); err != nil {
		t.Fatal(err)
	}
	if len(p.reqs) != 1 || len(compactedEvents(&c)) != 0 {
		t.Fatal("窗口未知时不应主动压缩")
	}
}

func TestNoCompactionWhenUnderBudget(t *testing.T) {
	p := &scripted{steps: []scriptStep{say("好")}}
	a := compactAgent(t, p, 100000, textTurns(3, 20))
	var c collector
	_ = a.Run(context.Background(), "q", &c)
	if len(p.reqs) != 1 || len(compactedEvents(&c)) != 0 {
		t.Fatal("没超预算不应压缩")
	}
}

func TestPruneAloneIsEnoughNoSummaryCall(t *testing.T) {
	// 历史里是几个很大的旧工具输出：裁掉它们就够了，不需要为此多调一次模型做摘要。
	h := []Message{{Role: RoleUser, Content: "旧问题"}}
	for i := 0; i < 4; i++ {
		h = append(h, Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c", Name: "bash", Arguments: "{}"}}},
			bigTool("c", 3000))
	}
	h = append(h, textTurns(4, 5)...)
	p := &scripted{steps: []scriptStep{say("好")}}
	a := compactAgent(t, p, 2000, h)
	var c collector
	if err := a.Run(context.Background(), "q", &c); err != nil {
		t.Fatal(err)
	}
	evs := compactedEvents(&c)
	if len(p.reqs) != 1 || len(evs) != 1 || evs[0].Mode != event.CompactPrune || evs[0].Dropped < 1 || a.Summary() != "" {
		t.Fatalf("只应裁剪、不应摘要: 请求 %d 次，事件 %+v", len(p.reqs), evs)
	}
	for _, m := range p.reqs[0].Messages {
		if m.Role == RoleTool && len([]rune(m.Content)) > 1000 && !strings.Contains(m.Content, "已裁剪") {
			t.Fatal("发给模型的历史里不应再有未裁剪的大输出")
		}
	}
}

func TestSummaryFailureKeepsHistoryAndTurnContinues(t *testing.T) {
	h := textTurns(8, 100)
	p := &scripted{steps: []scriptStep{
		{err: errors.New("摘要请求失败")}, // 摘要调用
		say("照常回答"),
		say("下一步"), // 不该到这：第二步不应再尝试摘要
	}}
	a := compactAgent(t, p, 800, h)
	var c collector

	if err := a.Run(context.Background(), "q", &c); err != nil {
		t.Fatalf("摘要失败不应让本轮失败: %v", err)
	}
	evs := compactedEvents(&c)
	if len(evs) != 1 || evs[0].Err == "" || evs[0].Dropped != 0 {
		t.Fatalf("应有一个带 Err 的 Compacted 事件: %+v", evs)
	}
	if len(a.History()) != len(h)+2 { // 原历史 + 本轮 user + assistant
		t.Errorf("摘要失败时历史必须保持原样（不能静默丢弃）: %d 条", len(a.History()))
	}
	if a.Summary() != "" || c.finishedReason(t) != event.ReasonStop {
		t.Errorf("summary=%q reason=%q", a.Summary(), c.finishedReason(t))
	}
}

func TestSummaryFailureNotRetriedEveryStep(t *testing.T) {
	// 一轮里有多步工具调用时，摘要失败后不应每一步都再试一次。
	p := &scripted{steps: []scriptStep{
		{err: errors.New("摘要失败")},
		callTools(call("c1", "bash", `{}`)),
		say("好"),
	}}
	a := newAgentCfg(t, Config{Provider: p, ContextWindow: 800, Retry: &RetryPolicy{},
		Tools: registry(t, okTool("bash", toolResultOK()))})
	a.Restore(textTurns(8, 100), "")
	if err := a.Run(context.Background(), "q", &collector{}); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range p.reqs {
		if isSummaryReq(r) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("摘要只应尝试 1 次，实际 %d", n)
	}
}

func TestReactiveCompactionAfterServerSaysTooLong(t *testing.T) {
	// 没配窗口（不会主动压缩），服务端报超长：压缩一次再重试，成功后本轮正常结束，且这次重试不占步数。
	tooLong := &ProviderError{Status: 400, Code: "context_length_exceeded", Message: "too long"}
	p := &scripted{steps: []scriptStep{{err: tooLong}, say("摘要内容"), say("最终回答")}}
	a := compactAgent(t, p, 0, textTurns(8, 100))
	var c collector

	if err := a.Run(context.Background(), "q", &c); err != nil {
		t.Fatalf("压缩后重试应成功: %v", err)
	}
	if len(p.reqs) != 3 || !isSummaryReq(p.reqs[1]) || isSummaryReq(p.reqs[2]) {
		t.Fatalf("请求序列不对: %d 次", len(p.reqs))
	}
	if len(p.reqs[2].Messages) >= len(p.reqs[0].Messages) || !strings.Contains(p.reqs[2].System, "摘要内容") {
		t.Error("重试的请求应使用压缩后的历史")
	}
	if c.has(event.KindError) || c.finishedReason(t) != event.ReasonStop {
		t.Errorf("事件 %v", c.kinds())
	}
}

func TestReactiveCompactionOnlyOncePerStep(t *testing.T) {
	// 压缩后服务端还是说超长：不能无限循环，直接报错。
	tooLong := &ProviderError{Status: 400, Code: "context_length_exceeded", Message: "too long"}
	p := &scripted{steps: []scriptStep{{err: tooLong}, say("摘要"), {err: tooLong}, say("不该到这")}}
	a := compactAgent(t, p, 0, textTurns(8, 100))
	var c collector

	err := a.Run(context.Background(), "q", &c)
	var pe *ProviderError
	if !errors.As(err, &pe) || !pe.ContextTooLong() {
		t.Fatalf("应返回超长错误: %v", err)
	}
	if len(p.reqs) != 3 {
		t.Fatalf("应恰好 3 次请求（失败、摘要、失败）: %d", len(p.reqs))
	}
}

func TestTooLongWithNothingToCompactJustFails(t *testing.T) {
	// 只有一轮、没什么可压的：直接把服务端的错误交给用户，不做无用功。
	tooLong := &ProviderError{Status: 400, Code: "context_length_exceeded"}
	p := &scripted{steps: []scriptStep{{err: tooLong}, say("不该到这")}}
	a := compactAgent(t, p, 0, nil)
	err := a.Run(context.Background(), "q", &collector{})
	if err == nil || len(p.reqs) != 1 {
		t.Fatalf("err=%v 请求 %d 次", err, len(p.reqs))
	}
}

func toolResultOK() tool.Result { return tool.Result{Command: "ls", Stdout: "ok"} }

func TestCutPointNeverSplitsToolCallFromItsResults(t *testing.T) {
	h := []Message{
		{Role: RoleUser, Content: "第一轮"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "a"}, {ID: "b"}}},
		{Role: RoleTool, ToolID: "a", Content: "ra"},
		{Role: RoleTool, ToolID: "b", Content: "rb"},
		{Role: RoleAssistant, Content: "第一轮结束"},
		{Role: RoleUser, Content: "第二轮"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c"}}},
		{Role: RoleTool, ToolID: "c", Content: "rc"},
		{Role: RoleAssistant, Content: "第二轮结束"},
	}
	a := &Agent{history: h}
	for _, target := range []int{1, 10, 100, 100000} {
		cut := a.cutPoint(target)
		if cut == 0 {
			continue
		}
		if h[cut].Role != RoleUser {
			t.Fatalf("target=%d 切点 %d 不是用户消息: %+v", target, cut, h[cut])
		}
		// 切点之后的每个 tool 结果，对应的 assistant tool_calls 也必须在切点之后。
		seen := map[string]bool{}
		for _, m := range h[cut:] {
			for _, c := range m.ToolCalls {
				seen[c.ID] = true
			}
			if m.Role == RoleTool && !seen[m.ToolID] {
				t.Fatalf("target=%d 切点 %d 拆开了 tool call 和它的结果 %q", target, cut, m.ToolID)
			}
		}
	}
}

func TestCutPointAlwaysKeepsAtLeastTheLastTurn(t *testing.T) {
	h := textTurns(3, 5000) // 每轮都远超预算
	a := &Agent{history: h}
	cut := a.cutPoint(100)
	if cut != 4 { // 最后一轮从下标 4 开始
		t.Fatalf("装不下时也要保留最后一轮，切点 = %d", cut)
	}
}

func TestCutPointWithSingleTurnCannotCut(t *testing.T) {
	if cut := (&Agent{history: textTurns(1, 100)}).cutPoint(10); cut != 0 {
		t.Fatalf("只有一轮时没法切，得到 %d", cut)
	}
	if cut := (&Agent{}).cutPoint(10); cut != 0 {
		t.Fatalf("空历史得到 %d", cut)
	}
}

func TestRequestTokensUsesServerReportedUsage(t *testing.T) {
	a := &Agent{history: textTurns(4, 100)}
	heuristic := a.requestTokens()
	// 服务端说前 6 条一共 500 token：之后只需估算新增的 2 条。
	a.calibTokens, a.calibLen = 500, 6
	want := 500 + estimateMessages(a.history[6:])
	if got := a.requestTokens(); got != want || got == heuristic {
		t.Fatalf("应基于服务端用量校准: %d (期望 %d，纯估算 %d)", got, want, heuristic)
	}
	a.resetCalibration()
	if a.requestTokens() != heuristic {
		t.Error("复位校准后应回到纯估算")
	}
	// 历史被缩短到校准点之前时不能越界，退回纯估算。
	a.calibTokens, a.calibLen = 500, 99
	if a.requestTokens() != heuristic {
		t.Error("校准点超过历史长度时应忽略校准")
	}
}

func TestSummaryRejectsTruncatedOrEmptyAnswers(t *testing.T) {
	cases := map[string]scriptStep{
		"被长度截断": {resp: Response{Message: Message{Content: "半截摘要"}, StopReason: StopLength}},
		"空摘要":   {resp: Response{Message: Message{Content: "   "}, StopReason: StopStop}},
	}
	for name, step := range cases {
		t.Run(name, func(t *testing.T) {
			p := &scripted{steps: []scriptStep{step, say("照常回答")}}
			a := compactAgent(t, p, 800, textTurns(8, 100))
			var c collector
			if err := a.Run(context.Background(), "q", &c); err != nil {
				t.Fatal(err)
			}
			evs := compactedEvents(&c)
			if len(evs) != 1 || evs[0].Err == "" || a.Summary() != "" {
				t.Fatalf("不合格的摘要不能被采用: %+v summary=%q", evs, a.Summary())
			}
		})
	}
}

func TestSecondCompactionMergesPreviousSummary(t *testing.T) {
	p := &scripted{steps: []scriptStep{say("合并后的新摘要"), say("回答")}}
	a := compactAgent(t, p, 800, textTurns(8, 100))
	a.Restore(textTurns(8, 100), "旧摘要内容")
	if err := a.Run(context.Background(), "q", &collector{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.reqs[0].Messages[0].Content, "此前的摘要") ||
		!strings.Contains(p.reqs[0].Messages[0].Content, "旧摘要内容") {
		t.Error("再次压缩时应把旧摘要一并交给摘要请求")
	}
	if a.Summary() != "合并后的新摘要" {
		t.Errorf("摘要应被新摘要取代: %q", a.Summary())
	}
}

func TestRenderForSummaryBoundsInputAndNotesDrops(t *testing.T) {
	msgs := textTurns(50, 100)
	out := renderForSummary("", msgs, 1000)
	if n := len([]rune(out)); n > 1400 {
		t.Errorf("渲染结果 %d 字符，应受上限约束", n)
	}
	if !strings.Contains(out, "更早的") || !strings.Contains(out, "未纳入") {
		t.Error("丢弃了更早的消息时要注明")
	}
	full := renderForSummary("", textTurns(1, 10), 100000)
	if strings.Contains(full, "未纳入") || !strings.Contains(full, "用户: ") || !strings.Contains(full, "助手: ") {
		t.Errorf("没超限时应完整保留: %q", full)
	}
	withTools := renderForSummary("", []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{Name: "bash", Arguments: `{"command":"ls"}`}}},
		{Role: RoleTool, Content: "输出"},
	}, 10000)
	if !strings.Contains(withTools, "[调用 bash]") || !strings.Contains(withTools, "[结果] 输出") {
		t.Errorf("工具调用和结果都要渲染: %q", withTools)
	}
}

func TestRestoreReplacesStateAndResetsCalibration(t *testing.T) {
	a := compactAgent(t, &scripted{}, 0, textTurns(2, 10))
	a.calibTokens, a.calibLen = 999, 2
	h := textTurns(1, 5)
	a.Restore(h, "摘要")
	h[0].Content = "被外部篡改"
	if got := a.History(); len(got) != 2 || got[0].Content == "被外部篡改" || a.Summary() != "摘要" {
		t.Errorf("Restore 应复制传入的历史: %+v", got)
	}
	if a.calibLen != 0 {
		t.Error("Restore 后应复位校准")
	}
}

func TestSummaryRequestOutputCapHasAFloor(t *testing.T) {
	// 真实 DeepSeek 上发现的问题：用户的 maxOutput 很小时，思考模式的推理 token 把摘要的输出额度吃光，摘要被截断。
	for configured, want := range map[int]int{0: 0, 600: minSummaryOutput, 8192: 8192, 384000: 384000} {
		if got := summaryMaxOutput(configured); got != want {
			t.Errorf("summaryMaxOutput(%d) = %d，期望 %d", configured, got, want)
		}
	}
	p := &scripted{steps: []scriptStep{say("摘要"), say("回答")}}
	a := newAgentCfg(t, Config{Provider: p, ContextWindow: 800, Retry: &RetryPolicy{}})
	a.cfg.MaxOutputTokens = 123
	a.Restore(textTurns(8, 100), "")
	if err := a.Run(context.Background(), "q", &collector{}); err != nil {
		t.Fatal(err)
	}
	if !isSummaryReq(p.reqs[0]) || p.reqs[0].MaxOutputTokens != minSummaryOutput {
		t.Errorf("摘要请求的输出上限 = %d，应不低于 %d", p.reqs[0].MaxOutputTokens, minSummaryOutput)
	}
	if p.reqs[1].MaxOutputTokens != 123 {
		t.Errorf("正式请求仍应使用用户配置的上限: %d", p.reqs[1].MaxOutputTokens)
	}
}
