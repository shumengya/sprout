package session

import (
	"encoding/json"
	"testing"

	"github.com/shumengya/sprout/internal/agent/event"
)

// 协议原始内容（带签名的思考块）必须落盘并在恢复后回到历史里，否则恢复会话后
// 同一模型的下一次请求会缺思考块被服务端拒绝；没有原始内容的记录不出现 native 字段。
func TestNativeIsRecordedAndReplayed(t *testing.T) {
	native := &event.Native{
		Protocol: "anthropic-messages", Model: "claude-x",
		Data: json.RawMessage(`[{"type":"thinking","thinking":"想","signature":"SIG"},{"type":"text","text":"答"}]`),
	}
	r, _ := newRec(t)
	r.Emit(event.TurnStarted{Prompt: "q"})
	r.Emit(event.MessageCompleted{Content: "答", Reasoning: "想", StopReason: "stop", Native: native})
	r.Emit(event.TurnFinished{Reason: event.ReasonStop})
	r.Emit(event.TurnStarted{Prompt: "q2"})
	r.Emit(event.MessageCompleted{Content: "答2", StopReason: "stop"})
	r.Emit(event.TurnFinished{Reason: event.ReasonStop})
	r.Close()

	recs := readRecords(t, r.Path())
	if n := recs[2]["native"]; n == nil {
		t.Fatalf("assistant 记录缺 native: %v", recs[2])
	}
	if _, has := recs[5]["native"]; has {
		t.Errorf("没有原始内容时不应出现 native 字段: %v", recs[5])
	}

	st, err := Load(r.Path())
	if err != nil {
		t.Fatal(err)
	}
	got := st.History[1].Native
	if got == nil || got.Protocol != native.Protocol || got.Model != native.Model {
		t.Fatalf("恢复出的 Native = %+v", got)
	}
	var a, b any
	_ = json.Unmarshal(got.Data, &a)
	_ = json.Unmarshal(native.Data, &b)
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Errorf("Native.Data = %s，想要 %s", got.Data, native.Data)
	}
	if st.History[3].Native != nil {
		t.Errorf("第二轮不该有 Native: %+v", st.History[3].Native)
	}

	// 换模型恢复：思维链和原始内容一起丢掉，对话内容保留。
	st.StripReasoning()
	if h := st.History[1]; h.Native != nil || h.Reasoning != "" || h.Text() != "答" {
		t.Errorf("StripReasoning 后 = %+v", h)
	}
}
