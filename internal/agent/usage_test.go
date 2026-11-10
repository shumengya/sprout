package agent

import (
	"context"
	"testing"

	"github.com/shumengya/sprout/internal/agent/event"
)

func TestUsageEventCarriesReasoningTokens(t *testing.T) {
	p := &scripted{steps: []scriptStep{{
		chunks: []string{"答"},
		resp: Response{Message: Message{Content: "答"}, StopReason: StopStop,
			Usage: Usage{InputTokens: 3, OutputTokens: 50, TotalTokens: 53, ReasoningTokens: 42}},
	}}}
	a := newAgent(t, p, nil, nil)
	var c collector
	if err := a.Run(context.Background(), "hi", &c); err != nil {
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
