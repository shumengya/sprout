package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent/event"
)

func manualEvents(c *collector) []event.Compacted {
	var out []event.Compacted
	for _, ce := range compactedEvents(c) {
		if ce.Manual {
			out = append(out, ce)
		}
	}
	return out
}

func TestManualCompactSummarizesTheWholeHistory(t *testing.T) {
	p := &scripted{steps: []scriptStep{say("手动摘要内容"), say("接着聊的回答")}}
	h := textTurns(4, 100)
	a := compactAgent(t, p, 0, h) // 窗口未知：手动压缩不依赖窗口
	var c collector

	if err := a.Compact(context.Background(), CompactOptions{}, &c); err != nil {
		t.Fatal(err)
	}
	if len(p.reqs) != 1 || !isSummaryReq(p.reqs[0]) || len(p.reqs[0].Tools) != 0 {
		t.Fatalf("应只发一次不带工具的摘要请求: %d 次", len(p.reqs))
	}
	if !strings.Contains(p.reqs[0].Messages[0].Text(), "问问问") {
		t.Error("摘要请求里应包含被压缩的对话")
	}
	if len(a.History()) != 0 || a.Summary() != "手动摘要内容" {
		t.Fatalf("历史应清空、摘要应是模型的回答: %d 条，%q", len(a.History()), a.Summary())
	}
	evs := manualEvents(&c)
	if len(evs) != 1 || len(compactedEvents(&c)) != 1 {
		t.Fatalf("应恰好一个手动压缩事件: %+v", compactedEvents(&c))
	}
	e := evs[0]
	if e.Mode != event.CompactSummary || e.Dropped != len(h) || e.Err != "" || e.Summary != "手动摘要内容" || e.TokensAfter >= e.TokensBefore {
		t.Errorf("事件 = %+v", e)
	}

	// 压缩之后接着聊：请求只带摘要和新消息，不再带旧历史。
	if err := a.Run(context.Background(), Input{Text: "接下来呢"}, &collector{}); err != nil {
		t.Fatal(err)
	}
	next := p.reqs[1]
	if len(next.Messages) != 1 || next.Messages[0].Text() != "接下来呢" || !strings.Contains(next.System, "手动摘要内容") {
		t.Errorf("压缩后的请求应只有摘要和新消息: %d 条，system=%.120q", len(next.Messages), next.System)
	}
}

func TestManualCompactFocusSteersTheSummaryPrompt(t *testing.T) {
	p := &scripted{steps: []scriptStep{say("摘要"), say("摘要2")}}
	a := compactAgent(t, p, 0, textTurns(2, 20))
	if err := a.Compact(context.Background(), CompactOptions{Focus: "  保留所有文件路径  "}, &collector{}); err != nil {
		t.Fatal(err)
	}
	sys := p.reqs[0].System
	if !strings.HasPrefix(sys, summarySystemPrompt) || !strings.HasSuffix(sys, "保留所有文件路径") {
		t.Errorf("要求应去掉首尾空白后追加在默认提示词之后: %q", sys)
	}

	// 说明过长要截断，免得把系统提示词撑大。
	a.Restore(textTurns(2, 20), "")
	if err := a.Compact(context.Background(), CompactOptions{Focus: strings.Repeat("长", maxFocusRunes*3)}, &collector{}); err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(p.reqs[1].System)); n > len([]rune(summarySystemPrompt))+maxFocusRunes+200 {
		t.Errorf("过长的说明应被截断，系统提示词有 %d 字符", n)
	}
}

func TestManualCompactWithoutFocusUsesTheDefaultPromptUnchanged(t *testing.T) {
	p := &scripted{steps: []scriptStep{say("摘要")}}
	a := compactAgent(t, p, 0, textTurns(2, 20))
	if err := a.Compact(context.Background(), CompactOptions{Focus: "   "}, &collector{}); err != nil {
		t.Fatal(err)
	}
	if !isSummaryReq(p.reqs[0]) {
		t.Errorf("空白的说明等于没有说明，应使用原样的摘要提示词: %q", p.reqs[0].System)
	}
}

func TestManualCompactWithEmptyHistoryIsRejected(t *testing.T) {
	p := &scripted{}
	a := compactAgent(t, p, 0, nil)
	var c collector
	if err := a.Compact(context.Background(), CompactOptions{}, &c); !errors.Is(err, ErrNothingToCompact) {
		t.Fatalf("err = %v，期望 ErrNothingToCompact", err)
	}
	if len(p.reqs) != 0 || len(c.events) != 0 {
		t.Errorf("没有对话时不该调用模型也不该发事件: 请求 %d 次，事件 %v", len(p.reqs), c.kinds())
	}

	// 只有摘要、没有新对话：同样没东西可压，别把摘要再压一遍。
	a.Restore(nil, "旧摘要")
	if err := a.Compact(context.Background(), CompactOptions{}, &c); !errors.Is(err, ErrNothingToCompact) || a.Summary() != "旧摘要" || len(p.reqs) != 0 {
		t.Errorf("只有摘要时应拒绝: err=%v summary=%q 请求 %d 次", err, a.Summary(), len(p.reqs))
	}
}

func TestManualCompactFailureKeepsEverythingAndReportsIt(t *testing.T) {
	p := &scripted{steps: []scriptStep{{err: errors.New("摘要请求失败")}}}
	h := textTurns(3, 50)
	a := compactAgent(t, p, 0, h)
	a.Restore(h, "已有摘要")
	var c collector

	err := a.Compact(context.Background(), CompactOptions{}, &c)
	if err == nil || !strings.Contains(err.Error(), "摘要请求失败") {
		t.Fatalf("应返回摘要失败的错误: %v", err)
	}
	if len(a.History()) != len(h) || a.Summary() != "已有摘要" {
		t.Errorf("失败时历史和摘要必须原样: %d 条，%q", len(a.History()), a.Summary())
	}
	evs := manualEvents(&c)
	if len(evs) != 1 || evs[0].Err == "" || evs[0].Dropped != 0 || evs[0].Summary != "" || evs[0].TokensAfter != evs[0].TokensBefore {
		t.Errorf("应有一个带 Err、没改动历史的手动压缩事件: %+v", evs)
	}
}

func TestManualCompactCancelledIsSilentAndChangesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &scripted{steps: []scriptStep{
		{hook: func(context.Context) { cancel() }, resp: Response{Message: NewTextMessage(RoleAssistant, "不该被采用"), StopReason: StopStop}},
	}}
	h := textTurns(3, 50)
	a := compactAgent(t, p, 0, h)
	a.Restore(h, "已有摘要")
	var c collector

	if err := a.Compact(ctx, CompactOptions{}, &c); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v，期望 context.Canceled", err)
	}
	if len(c.events) != 0 {
		t.Errorf("被取消不该发任何事件: %v", c.kinds())
	}
	if len(a.History()) != len(h) || a.Summary() != "已有摘要" {
		t.Errorf("被取消时历史和摘要必须原样: %d 条，%q", len(a.History()), a.Summary())
	}
}

func TestManualCompactAppendsALayerToTheExistingSummary(t *testing.T) {
	p := &scripted{steps: []scriptStep{say("新一层")}}
	a := compactAgent(t, p, 0, textTurns(2, 30))
	a.Restore(textTurns(2, 30), "旧摘要")
	if err := a.Compact(context.Background(), CompactOptions{}, &collector{}); err != nil {
		t.Fatal(err)
	}
	if want := joinSummary([]string{"旧摘要", "新一层"}); a.Summary() != want {
		t.Errorf("摘要 = %q，期望旧层加新层 %q", a.Summary(), want)
	}
	if strings.Contains(p.reqs[0].Messages[0].Text(), "旧摘要") {
		t.Error("旧摘要不该再交给摘要请求重写")
	}
}

func TestManualCompactResetsCalibration(t *testing.T) {
	// 历史没了，之前服务端报告的用量对不上了；不复位的话下一次估算会基于一个过期的校准点。
	p := &scripted{steps: []scriptStep{say("摘要")}}
	a := compactAgent(t, p, 0, textTurns(2, 30))
	a.calibTokens, a.calibLen = 5000, 4
	if err := a.Compact(context.Background(), CompactOptions{}, &collector{}); err != nil {
		t.Fatal(err)
	}
	if a.calibLen != 0 || a.calibTokens != 0 {
		t.Errorf("压缩后应复位校准: calibTokens=%d calibLen=%d", a.calibTokens, a.calibLen)
	}
}

func TestManualCompactGoesToTheRecorderToo(t *testing.T) {
	// 会话落盘靠 Config.Recorder：手动压缩的事件必须同样发给它，否则恢复出来的历史与运行时不一致。
	var rec collector
	p := &scripted{steps: []scriptStep{say("摘要")}}
	a := newAgentCfg(t, Config{Provider: p, Recorder: &rec, Retry: &RetryPolicy{}})
	a.Restore(textTurns(2, 30), "")
	if err := a.Compact(context.Background(), CompactOptions{}, nil); err != nil { // sink 为 nil 也要能用
		t.Fatal(err)
	}
	if evs := compactedEvents(&rec); len(evs) != 1 || !evs[0].Manual {
		t.Errorf("Recorder 应收到手动压缩事件: %+v", evs)
	}
}

func TestManualCompactKeepLastKeepsTheLatestTurnVerbatim(t *testing.T) {
	p := &scripted{steps: []scriptStep{say("前几轮的摘要"), say("接着聊的回答")}}
	h := textTurns(3, 50)
	last := []Message{
		{Role: RoleUser, Content: []Block{TextBlock("最近一轮的提问")}},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "bash", Arguments: `{"command":"ls"}`}},
			Native: &Native{Protocol: "anthropic-messages", Model: "m", Data: []byte(`[]`)}},
		{Role: RoleTool, ToolID: "c1", Content: []Block{TextBlock("最近一轮的工具输出")}},
		{Role: RoleAssistant, Content: []Block{TextBlock("最近一轮的回答")}},
	}
	a := compactAgent(t, p, 0, append(h, last...))
	var c collector

	if err := a.Compact(context.Background(), CompactOptions{KeepLast: true}, &c); err != nil {
		t.Fatal(err)
	}
	req := p.reqs[0].Messages[0].Text()
	if !strings.Contains(req, "问问问") || strings.Contains(req, "最近一轮") {
		t.Errorf("摘要请求应只含最近一轮之前的对话: %.200q", req)
	}
	got := a.History()
	if len(got) != len(last) || got[0].Text() != "最近一轮的提问" || got[2].Text() != "最近一轮的工具输出" || got[3].Text() != "最近一轮的回答" {
		t.Fatalf("应原样保留最近一轮: %d 条 %+v", len(got), got)
	}
	if got[1].Native != nil {
		t.Error("保留下来的消息前面换成了摘要，原始思考块对不上前缀，应丢掉")
	}
	if a.Summary() != "前几轮的摘要" {
		t.Errorf("摘要 = %q", a.Summary())
	}
	evs := manualEvents(&c)
	if len(evs) != 1 || evs[0].Dropped != len(h) || evs[0].Err != "" {
		t.Fatalf("事件的 Dropped 应是被摘要替代的条数 %d: %+v", len(h), evs)
	}

	// 接着聊：请求带摘要、最近一轮原文和新消息。
	if err := a.Run(context.Background(), Input{Text: "然后呢"}, &collector{}); err != nil {
		t.Fatal(err)
	}
	next := p.reqs[1]
	if len(next.Messages) != len(last)+1 || next.Messages[0].Text() != "最近一轮的提问" || !strings.Contains(next.System, "前几轮的摘要") {
		t.Errorf("压缩后的请求应是摘要 + 最近一轮 + 新消息: %d 条", len(next.Messages))
	}
}

func TestManualCompactKeepLastWithOnlyOneTurnIsRejected(t *testing.T) {
	p := &scripted{}
	a := compactAgent(t, p, 0, textTurns(1, 20))
	var c collector
	if err := a.Compact(context.Background(), CompactOptions{KeepLast: true}, &c); !errors.Is(err, ErrOnlyLastTurn) {
		t.Fatalf("err = %v，期望 ErrOnlyLastTurn", err)
	}
	if len(p.reqs) != 0 || len(c.events) != 0 || len(a.History()) != 2 {
		t.Errorf("不该调用模型、发事件或改历史: 请求 %d 次，事件 %v，历史 %d 条", len(p.reqs), c.kinds(), len(a.History()))
	}
}
