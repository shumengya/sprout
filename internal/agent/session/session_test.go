package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shumengya/sprout/internal/agent/event"
)

var testInfo = Info{Provider: "deepseek", Model: "deepseek-chat", Cwd: `D:\SmyProjects\sprout`, Tools: []string{"powershell", "bash"}, Version: "test"}

func newRec(t *testing.T) (*Recorder, string) {
	t.Helper()
	dir := t.TempDir()
	r := NewRecorder(dir, testInfo, nil)
	t.Cleanup(func() { r.Close() })
	return r, dir
}

func readRecords(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("会话文件的一行不是合法 JSON: %v\n%s", err, sc.Text())
		}
		out = append(out, m)
	}
	return out
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		`D:\SmyProjects\sprout`: "--D--SmyProjects-sprout--",
		`/home/a/b`:             "--home-a-b--",
		`C:\`:                   "--C----",
		`\\server\share\x`:      "--server-share-x--",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestFileIsCreatedLazily(t *testing.T) {
	r, dir := newRec(t)
	r.Emit(event.MessageDelta{Text: "x"}) // 不落盘的事件不应触发建文件
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("没有可落盘事件时不应创建任何文件: %v", entries)
	}
	if r.Path() != "" {
		t.Error("Path 应为空")
	}
	r.Emit(event.TurnStarted{Prompt: "hi"})
	if r.Path() == "" {
		t.Fatal("第一个可落盘事件应创建文件")
	}
}

func TestRecordsFullTurn(t *testing.T) {
	r, dir := newRec(t)
	for _, e := range []event.Event{
		event.TurnStarted{Prompt: "看一下目录"},
		event.MessageDelta{Text: "不应落盘"},
		event.MessageCompleted{ToolCalls: []event.ToolCall{{ID: "c1", Name: "powershell", Arguments: `{"command":"ls"}`}}, StopReason: "tool_calls"},
		event.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		event.ToolRequested{Call: event.ToolCall{ID: "c1"}},
		event.ToolFinished{ID: "c1", Name: "powershell", Command: "ls", Stdout: "a.go\n", ExitCode: 0, Duration: 1500 * time.Millisecond},
		event.MessageCompleted{Content: "入口是 a.go", StopReason: "stop"},
		event.TurnFinished{Reason: event.ReasonStop},
	} {
		r.Emit(e)
	}
	r.Close()

	recs := readRecords(t, r.Path())
	var types []string
	for _, m := range recs {
		types = append(types, m["type"].(string))
	}
	want := "session,user,assistant,usage,tool,assistant,turn"
	if strings.Join(types, ",") != want {
		t.Fatalf("记录类型 = %v，期望 %s（delta 与 ToolRequested 不落盘）", types, want)
	}

	if recs[0]["cwd"] != testInfo.Cwd || recs[0]["model"] != "deepseek-chat" || recs[0]["provider"] != "deepseek" {
		t.Errorf("session 头 = %v", recs[0])
	}
	if recs[1]["content"] != "看一下目录" {
		t.Errorf("user = %v", recs[1])
	}
	calls := recs[2]["tool_calls"].([]any)
	if c := calls[0].(map[string]any); c["id"] != "c1" || c["arguments"] != `{"command":"ls"}` {
		t.Errorf("assistant.tool_calls = %v", calls)
	}
	tr := recs[4]
	if tr["tool_call_id"] != "c1" || tr["stdout"] != "a.go\n" || tr["duration_ms"] != float64(1500) {
		t.Errorf("tool = %v", tr)
	}
	if v, ok := tr["exit_code"]; !ok || v != float64(0) {
		t.Errorf("exit_code 为 0 时也必须出现: %v", tr)
	}
	if recs[6]["reason"] != "stop" {
		t.Errorf("turn = %v", recs[6])
	}
	for _, m := range recs {
		if _, err := time.Parse(time.RFC3339Nano, m["time"].(string)); err != nil {
			t.Errorf("time 字段格式不对: %v", m["time"])
		}
	}
	// 路径形如 <dir>/--slug--/<时间戳>_<id>.jsonl。
	rel, _ := filepath.Rel(dir, r.Path())
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) != 2 || parts[0] != Slug(testInfo.Cwd) || !strings.HasSuffix(parts[1], ".jsonl") || !strings.Contains(parts[1], "_") {
		t.Errorf("会话路径 = %s", rel)
	}
}

func TestReasoningIsRecordedButDeltasAreNot(t *testing.T) {
	r, _ := newRec(t)
	r.Emit(event.TurnStarted{Prompt: "q"})
	r.Emit(event.ReasoningDelta{Text: "增量不落盘"})
	r.Emit(event.MessageCompleted{Content: "答", Reasoning: "完整思维链", StopReason: "stop"})
	r.Emit(event.MessageCompleted{Content: "无思维链"})
	r.Close()

	recs := readRecords(t, r.Path())
	if len(recs) != 4 { // session, user, assistant, assistant
		t.Fatalf("记录数 = %d，ReasoningDelta 不应落盘", len(recs))
	}
	if recs[2]["reasoning"] != "完整思维链" {
		t.Errorf("assistant 记录应保存完整思维链: %v", recs[2])
	}
	if _, has := recs[3]["reasoning"]; has {
		t.Errorf("没有思维链时不应出现 reasoning 字段: %v", recs[3])
	}
}

func TestChineseAndNewlinesSurviveRoundTrip(t *testing.T) {
	r, _ := newRec(t)
	r.Emit(event.TurnStarted{Prompt: "你好\n世界 \"引号\" \\反斜杠"})
	r.Close()
	recs := readRecords(t, r.Path())
	if recs[1]["content"] != "你好\n世界 \"引号\" \\反斜杠" {
		t.Fatalf("得到 %q", recs[1]["content"])
	}
}

func TestMetaJSON(t *testing.T) {
	r, dir := newRec(t)
	r.Emit(event.TurnStarted{Prompt: "q"})
	r2 := NewRecorder(dir, testInfo, nil) // 同一个工作目录的第二个会话
	r2.now = func() time.Time { return time.Now().Add(time.Second) }
	r2.Emit(event.TurnStarted{Prompt: "q2"})
	r2.Close()

	raw, err := os.ReadFile(filepath.Join(dir, Slug(testInfo.Cwd), "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var gm groupMeta
	if err := json.Unmarshal(raw, &gm); err != nil {
		t.Fatalf("meta.json 不是合法 JSON: %v", err)
	}
	if gm.Cwd != testInfo.Cwd || len(gm.Sessions) != 2 {
		t.Fatalf("meta = %+v，应同时登记两个会话", gm)
	}
	sm, ok := gm.Sessions[filepath.Base(r.Path())]
	if !ok || sm.Provider != "deepseek" || sm.Model != "deepseek-chat" || len(sm.Tools) != 2 || sm.StartedAt.IsZero() {
		t.Errorf("会话条目 = %+v (存在=%v)", sm, ok)
	}
}

func TestCorruptMetaIsRebuilt(t *testing.T) {
	r, dir := newRec(t)
	group := filepath.Join(dir, Slug(testInfo.Cwd))
	if err := os.MkdirAll(group, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(group, "meta.json"), []byte("{坏掉了"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.Emit(event.TurnStarted{Prompt: "q"})
	raw, _ := os.ReadFile(filepath.Join(group, "meta.json"))
	var gm groupMeta
	if json.Unmarshal(raw, &gm) != nil || len(gm.Sessions) != 1 {
		t.Fatalf("损坏的 meta.json 应被重建: %s", raw)
	}
	if len(readRecords(t, r.Path())) == 0 {
		t.Error("meta.json 损坏不应影响会话记录")
	}
}

func TestWriteFailureDoesNotPanicOrBlockAgent(t *testing.T) {
	// 会话根目录被一个普通文件占用，MkdirAll 必然失败。
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewRecorder(filepath.Join(blocker, "sessions"), testInfo, nil)
	r.Emit(event.TurnStarted{Prompt: "q"})
	r.Emit(event.TurnFinished{Reason: "stop"}) // 失败后继续收事件不应 panic
	if r.Err() == nil || r.Path() != "" {
		t.Fatalf("应记录首个错误且不留文件: err=%v path=%q", r.Err(), r.Path())
	}
}

func TestEmitAfterCloseAndDoubleClose(t *testing.T) {
	r, _ := newRec(t)
	r.Emit(event.TurnStarted{Prompt: "q"})
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	r.Emit(event.TurnFinished{Reason: "stop"})
	if err := r.Close(); err != nil {
		t.Fatalf("重复 Close 应无害: %v", err)
	}
	if n := len(readRecords(t, r.Path())); n != 2 {
		t.Fatalf("Close 之后的事件不应再写入，记录数 %d", n)
	}
}

func TestConcurrentEmit(t *testing.T) {
	r, _ := newRec(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Emit(event.MessageCompleted{Content: strings.Repeat("字", 1000)})
		}()
	}
	wg.Wait()
	r.Close()
	// 每行都必须是完整 JSON，不能交错。
	if n := len(readRecords(t, r.Path())); n != 21 {
		t.Fatalf("记录数 = %d，期望 21（1 个头 + 20 条）", n)
	}
}

func TestFilePermissionsAreRestricted(t *testing.T) {
	r, _ := newRec(t)
	r.Emit(event.TurnStarted{Prompt: "q"})
	st, err := os.Stat(r.Path())
	if err != nil {
		t.Fatal(err)
	}
	// Windows 不支持 Unix 权限位，只在支持的平台上断言。
	if perm := st.Mode().Perm(); os.PathSeparator == '/' && perm&0o077 != 0 {
		t.Errorf("会话文件权限 = %o，不应对其他用户开放", perm)
	}
}

func TestRetryIsRecordedForAudit(t *testing.T) {
	r, _ := newRec(t)
	r.Emit(event.TurnStarted{Prompt: "q"})
	r.Emit(event.Retrying{Attempt: 1, Max: 3, Delay: 1500 * time.Millisecond, Reason: "HTTP 503"})
	r.Close()
	recs := readRecords(t, r.Path())
	last := recs[len(recs)-1]
	if last["type"] != "retry" || last["attempt"] != float64(1) || last["delay_ms"] != float64(1500) || last["reason"] != "HTTP 503" {
		t.Fatalf("retry 记录 = %v", last)
	}
}
