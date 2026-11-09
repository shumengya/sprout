package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/event"
)

// writeSession 把若干行原始 JSONL 写成一个会话文件，用来构造进程被杀、旧格式、损坏等现场。
func writeSession(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "20260101-000000_abcd1234.jsonl")
	body := `{"type":"session","time":"2026-01-01T00:00:00Z","id":"abcd1234","cwd":"D:\\p","provider":"deepseek","model":"deepseek-flash","tools":["bash"]}` + "\n" +
		strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func load(t *testing.T, lines ...string) *State {
	t.Helper()
	st, err := Load(writeSession(t, lines...))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func roles(h []agent.Message) string {
	var r []string
	for _, m := range h {
		r = append(r, string(m.Role))
	}
	return strings.Join(r, ",")
}

const (
	u1   = `{"type":"user","content":"问一"}`
	a1   = `{"type":"assistant","content":"答一","stop_reason":"stop"}`
	t1   = `{"type":"turn","reason":"stop"}`
	call = `{"type":"assistant","content":"","tool_calls":[{"id":"c1","name":"bash","arguments":"{}"},{"id":"c2","name":"bash","arguments":"{}"}]}`
	r1   = `{"type":"tool","tool_call_id":"c1","name":"bash","content":"结果1","exit_code":0}`
	r2   = `{"type":"tool","tool_call_id":"c2","name":"bash","content":"结果2","exit_code":0}`
)

func TestLoadSimpleConversation(t *testing.T) {
	st := load(t, u1, a1, t1, `{"type":"user","content":"问二"}`, `{"type":"assistant","content":"答二"}`, t1)
	if roles(st.History) != "user,assistant,user,assistant" || st.Turns != 2 || st.ID != "abcd1234" {
		t.Fatalf("历史 %s，轮数 %d，id %s", roles(st.History), st.Turns, st.ID)
	}
	if st.Info.Model != "deepseek-flash" || st.Info.Provider != "deepseek" {
		t.Errorf("Info = %+v", st.Info)
	}
}

func TestKilledMidToolsGetsInterruptedResults(t *testing.T) {
	// 进程在两个并行工具只完成一个时被杀：缺失的那个必须补上结果，否则下一次请求会被服务端拒绝。
	st := load(t, u1, call, r2) // 没有 turn 记录，c1 没有结果
	if roles(st.History) != "user,assistant,tool,tool" {
		t.Fatalf("历史 = %s", roles(st.History))
	}
	if st.History[2].ToolID != "c1" || !strings.Contains(st.History[2].Content, "被中断") ||
		st.History[3].ToolID != "c2" || st.History[3].Content != "结果2" {
		t.Errorf("tool 消息应按调用顺序，缺失的补“被中断”: %+v", st.History[2:])
	}
	last := st.Events[len(st.Events)-1].(event.TurnFinished)
	if last.Reason != event.ReasonCancelled {
		t.Errorf("没有 turn 记录的最后一轮应当作被取消: %+v", last)
	}
}

func TestToolResultsKeepCallOrderNotRecordOrder(t *testing.T) {
	st := load(t, u1, call, r2, r1, `{"type":"assistant","content":"完"}`, t1)
	if st.History[2].ToolID != "c1" || st.History[3].ToolID != "c2" {
		t.Fatalf("记录里 c2 先完成，历史里仍须 c1,c2: %+v", st.History[2:4])
	}
}

func TestUnansweredFirstCallIsRolledBack(t *testing.T) {
	// 首次模型调用就失败/被取消：运行时会撤销用户消息，重放也必须撤销。
	for _, reason := range []string{"error", "cancelled"} {
		st := load(t, u1, a1, t1, `{"type":"user","content":"失败的提问"}`, `{"type":"turn","reason":"`+reason+`"}`)
		if roles(st.History) != "user,assistant" {
			t.Errorf("%s: 历史 = %s", reason, roles(st.History))
		}
	}
}

func TestUserOnlyAtEndOfFileIsDropped(t *testing.T) {
	st := load(t, u1, a1, t1, `{"type":"user","content":"还没得到回应就被杀了"}`)
	if roles(st.History) != "user,assistant" {
		t.Fatalf("悬空的用户消息应丢弃: %s", roles(st.History))
	}
}

func TestInternalErrorDiscardsTheWholeTurn(t *testing.T) {
	// panic：运行时把历史整个退回本轮开始前（即使已经有了 assistant 和 tool 消息）。
	st := load(t, u1, a1, t1,
		`{"type":"user","content":"会 panic"}`, call, r1,
		`{"type":"error","message":"内部错误: 炸了"}`, `{"type":"turn","reason":"error"}`)
	if roles(st.History) != "user,assistant" {
		t.Fatalf("整轮应作废: %s", roles(st.History))
	}
}

func TestLaterStepFailureKeepsEarlierToolExchange(t *testing.T) {
	// 第二步模型调用失败：历史以 tool 结果结尾，仍是合法序列，不能被撤销。
	st := load(t, u1, call, r1, r2, `{"type":"error","message":"认证失败"}`, `{"type":"turn","reason":"error"}`)
	if roles(st.History) != "user,assistant,tool,tool" {
		t.Fatalf("历史 = %s", roles(st.History))
	}
}

func TestCorruptLinesAreSkippedAndCounted(t *testing.T) {
	st := load(t, u1, `{"type":"assistant","content":"半`, "not json at all", a1, t1)
	if st.Skipped != 2 || roles(st.History) != "user,assistant" {
		t.Fatalf("skipped=%d 历史=%s", st.Skipped, roles(st.History))
	}
}

func TestLegacyToolRecordWithoutContent(t *testing.T) {
	st := load(t, u1, call,
		`{"type":"tool","tool_call_id":"c1","name":"bash","stdout":"旧输出","exit_code":0}`,
		`{"type":"tool","tool_call_id":"c2","name":"bash","error":"起不来"}`, t1)
	if st.History[2].Content != "旧输出" || st.History[3].Content != "工具执行失败: 起不来" {
		t.Errorf("旧记录应尽力还原: %+v", st.History[2:])
	}
}

func TestOrphanToolRecordIsIgnored(t *testing.T) {
	st := load(t, u1, a1, `{"type":"tool","tool_call_id":"ghost","name":"bash","content":"孤立"}`, t1)
	if roles(st.History) != "user,assistant" {
		t.Fatalf("找不到 tool call 的 tool 记录不能进历史: %s", roles(st.History))
	}
}

func TestReplayEventsMatchWhatTUIExpects(t *testing.T) {
	st := load(t, u1, `{"type":"assistant","content":"","reasoning":"想","tool_calls":[{"id":"c1","name":"bash","arguments":"{\"command\":\"ls\"}"}]}`,
		`{"type":"tool","tool_call_id":"c1","name":"bash","command":"ls","stdout":"a\n","content":"x","duration_ms":1500}`,
		`{"type":"usage","input_tokens":10,"output_tokens":5,"total_tokens":15}`,
		`{"type":"assistant","content":"完"}`, t1)
	var ks []string
	for _, e := range st.Events {
		ks = append(ks, string(e.Kind()))
	}
	want := "turn.started,message.completed,tool.requested,tool.finished,usage,message.completed,turn.finished"
	if strings.Join(ks, ",") != want {
		t.Fatalf("事件 = %s", strings.Join(ks, ","))
	}
	fin := st.Events[3].(event.ToolFinished)
	if fin.Stdout != "a\n" || fin.Duration != 1500*time.Millisecond || fin.Command != "ls" {
		t.Errorf("ToolFinished = %+v", fin)
	}
	if mc := st.Events[1].(event.MessageCompleted); mc.Reasoning != "想" || len(mc.ToolCalls) != 1 {
		t.Errorf("MessageCompleted = %+v", mc)
	}
}

func TestStripReasoning(t *testing.T) {
	st := load(t, u1, `{"type":"assistant","content":"答","reasoning":"别家的思维链"}`, t1)
	st.StripReasoning()
	if st.History[1].Reasoning != "" || st.History[1].Content != "答" {
		t.Errorf("换模型后应清掉思维链、保留正文: %+v", st.History[1])
	}
}

func TestRetryAndResumeRecordsDoNotAffectHistory(t *testing.T) {
	st := load(t, u1, `{"type":"retry","attempt":1,"max":3,"delay_ms":1000,"reason":"x"}`, a1, t1,
		`{"type":"resume","provider":"p","model":"m","tools":[]}`)
	if roles(st.History) != "user,assistant" || st.Turns != 1 {
		t.Fatalf("历史 %s 轮数 %d", roles(st.History), st.Turns)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.jsonl")); err == nil {
		t.Fatal("文件不存在应报错")
	}
}
