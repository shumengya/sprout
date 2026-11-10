package session

import (
	"testing"

	"github.com/shumengya/sprout/internal/agent/event"
)

func TestReasoningTokensRoundTrip(t *testing.T) {
	r, _ := newRec(t)
	r.Emit(event.TurnStarted{Prompt: "q"})
	r.Emit(event.Usage{InputTokens: 10, OutputTokens: 50, TotalTokens: 60, ReasoningTokens: 42})
	r.Emit(event.Usage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3}) // 服务端没报告
	r.Close()

	var usages []map[string]any
	for _, rec := range readRecords(t, r.Path()) {
		if rec["type"] == "usage" {
			usages = append(usages, rec)
		}
	}
	if len(usages) != 2 {
		t.Fatalf("usage 记录 = %v", usages)
	}
	if usages[0]["reasoning_tokens"] != float64(42) {
		t.Errorf("应记录 reasoning_tokens: %v", usages[0])
	}
	if _, ok := usages[1]["reasoning_tokens"]; ok {
		t.Errorf("没报告时不写这个字段: %v", usages[1])
	}
}

func TestReplayCarriesReasoningTokens(t *testing.T) {
	st := load(t, u1, a1,
		`{"type":"usage","input_tokens":10,"output_tokens":50,"total_tokens":60,"reasoning_tokens":42}`, t1)
	var got []event.Usage
	for _, e := range st.Events {
		if u, ok := e.(event.Usage); ok {
			got = append(got, u)
		}
	}
	if len(got) != 1 || got[0].ReasoningTokens != 42 || got[0].TotalTokens != 60 {
		t.Fatalf("回放的 Usage = %+v", got)
	}
}

func TestOldSessionWithoutReasoningTokensStillLoads(t *testing.T) {
	st := load(t, u1, a1, `{"type":"usage","input_tokens":10,"output_tokens":5,"total_tokens":15}`, t1)
	for _, e := range st.Events {
		if u, ok := e.(event.Usage); ok && (u.ReasoningTokens != 0 || u.TotalTokens != 15) {
			t.Fatalf("旧会话的 Usage = %+v", u)
		}
	}
}
