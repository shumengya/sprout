package event

import "testing"

func TestKinds(t *testing.T) {
	cases := map[Kind]Event{
		KindTurnStarted:        TurnStarted{},
		KindUserQueued:         UserQueued{},
		KindMessageDelta:       MessageDelta{},
		KindReasoningDelta:     ReasoningDelta{},
		KindMessageCompleted:   MessageCompleted{},
		KindToolRequested:      ToolRequested{},
		KindToolFinished:       ToolFinished{},
		KindUsage:              Usage{},
		KindRetrying:           Retrying{},
		KindCompacted:          Compacted{},
		KindTurnFinished:       TurnFinished{},
		KindError:              Error{},
		KindBackgroundFinished: BackgroundFinished{},
	}
	for want, e := range cases {
		if got := e.Kind(); got != want {
			t.Errorf("%T.Kind() = %q，期望 %q", e, got, want)
		}
	}
}

func TestMultiSkipsNilAndKeepsOrder(t *testing.T) {
	var got []string
	a := SinkFunc(func(e Event) { got = append(got, "a:"+string(e.Kind())) })
	b := SinkFunc(func(e Event) { got = append(got, "b:"+string(e.Kind())) })

	Multi(a, nil, b).Emit(MessageDelta{Text: "x"})

	want := []string{"a:message.delta", "b:message.delta"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v，期望 %v", got, want)
	}
}
