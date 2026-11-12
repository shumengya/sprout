package agent

import (
	"context"
	"testing"

	"github.com/shumengya/sprout/internal/agent/event"
)

func TestUsageEventCarriesReasoningTokens(t *testing.T) {
	p := &scripted{steps: []scriptStep{{
		chunks: []string{"答"},
		resp: Response{Message: Message{Content: []Block{TextBlock("答")}}, StopReason: StopStop,
			Usage: Usage{InputTokens: 3, OutputTokens: 50, TotalTokens: 53, ReasoningTokens: 42}},
	}}}
	a := newAgent(t, p, nil, nil)
	var c collector
	if err := a.Run(context.Background(), Input{Text: "hi"}, &c); err != nil {
		t.Fatal(err)
	}
	for _, e := range c.events {
		if u, ok := e.(event.Usage); ok {
			if u.ReasoningTokens != 42 || u.OutputTokens != 50 {
				t.Fatalf("Usage = %+v", u)
			}
			return
		}
	}
	t.Fatal("没有 Usage 事件")
}

func TestMessageCompletedCarriesReasoningEstimate(t *testing.T) {
	p := &scripted{steps: []scriptStep{{
		chunks: []string{"答"},
		resp:   Response{Message: Message{Content: []Block{TextBlock("答")}, Reasoning: "先看一下目录结构"}, StopReason: StopStop},
	}}}
	a := newAgent(t, p, nil, nil)
	var c collector
	if err := a.Run(context.Background(), Input{Text: "hi"}, &c); err != nil {
		t.Fatal(err)
	}
	for _, e := range c.events {
		if mc, ok := e.(event.MessageCompleted); ok {
			if want := EstimateTokens("先看一下目录结构"); mc.ReasoningTokensEst != want || want == 0 {
				t.Fatalf("ReasoningTokensEst = %d，期望 %d", mc.ReasoningTokensEst, want)
			}
			return
		}
	}
	t.Fatal("没有 MessageCompleted 事件")
}

func TestUsageEventCarriesCacheTokens(t *testing.T) {
	p := &scripted{steps: []scriptStep{{
		chunks: []string{"答"},
		resp: Response{Message: Message{Content: []Block{TextBlock("答")}}, StopReason: StopStop,
			Usage: Usage{InputTokens: 350, OutputTokens: 5, TotalTokens: 355, CacheReadTokens: 300, CacheWriteTokens: 40}},
	}}}
	a := newAgent(t, p, nil, nil)
	var c collector
	if err := a.Run(context.Background(), Input{Text: "hi"}, &c); err != nil {
		t.Fatal(err)
	}
	for _, e := range c.events {
		if u, ok := e.(event.Usage); ok {
			if u.InputTokens != 350 || u.CacheReadTokens != 300 || u.CacheWriteTokens != 40 {
				t.Fatalf("Usage = %+v", u)
			}
			return
		}
	}
	t.Fatal("没有 Usage 事件")
}
