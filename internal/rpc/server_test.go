package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/event"
)

// fakeBackend 是测试替身：Run 按 script 发事件；block 为 true 时一直等到被取消。
type fakeBackend struct {
	mu       sync.Mutex
	disabled string
	block    bool
	inputs   []agent.Input
	newCount int
	prepared []string
	sessions []SessionInfo
	renamed  string
	deleted  []string
	branched []int
	compact  string
	model    string
	effort   string
}

func (f *fakeBackend) Info() Info {
	model := "p/m"
	if f.model != "" {
		model = f.model
	}
	return Info{Model: model, Effort: f.effort, Tools: []string{"bash"}, Available: f.disabled == "", Disabled: f.disabled}
}

func (f *fakeBackend) Prepare() []string {
	p := f.prepared
	f.prepared = nil
	return p
}

func (f *fakeBackend) Run(ctx context.Context, in agent.Input, sink event.Sink) error {
	f.mu.Lock()
	f.inputs = append(f.inputs, in)
	f.mu.Unlock()
	sink.Emit(event.TurnStarted{Prompt: in.Text})
	if f.block {
		<-ctx.Done()
		sink.Emit(event.TurnFinished{Reason: event.ReasonCancelled})
		return ctx.Err()
	}
	sink.Emit(event.MessageDelta{Text: "答"})
	sink.Emit(event.MessageCompleted{Content: "答", StopReason: "stop"})
	sink.Emit(event.ToolFinished{ID: "c1", Duration: 1500 * time.Millisecond, Stdout: "<a&b>"})
	sink.Emit(event.TurnFinished{Reason: event.ReasonStop})
	return nil
}

func (f *fakeBackend) NewSession() error {
	if f.disabled != "" {
		return errors.New("不可用")
	}
	f.newCount++
	return nil
}

func (f *fakeBackend) ListSessions(all bool) ([]SessionInfo, error) {
	if all {
		return append(f.sessions, SessionInfo{ID: "other", Path: "/s/other.jsonl"}), nil
	}
	return f.sessions, nil
}

func (f *fakeBackend) ResumeSession(selector string) (Restored, error) {
	if selector != "1" {
		return Restored{}, errors.New("找不到会话 " + selector)
	}
	return Restored{ID: "abc", Path: "/s/abc.jsonl", Notice: "已恢复会话 abc（1 轮）", Turns: 1, Events: []event.Event{
		event.TurnStarted{Prompt: "旧问题"},
		event.MessageCompleted{Content: "旧回答", StopReason: "stop"},
		event.TurnFinished{Reason: event.ReasonStop},
	}}, nil
}

func (f *fakeBackend) BranchSession(turns int) (Restored, error) {
	if turns > 1 {
		return Restored{}, errors.New("会话只有 1 轮")
	}
	f.branched = append(f.branched, turns)
	return Restored{ID: "new", Path: "/s/new.jsonl", Notice: "已分支", Turns: 1, Events: []event.Event{
		event.TurnStarted{Prompt: "旧问题"},
		event.TurnFinished{Reason: event.ReasonStop},
	}}, nil
}

func (f *fakeBackend) RenameSession(path, name string) error {
	f.renamed = path + "=" + name
	return nil
}

func (f *fakeBackend) DeleteSession(path string) error {
	if path == "/s/current.jsonl" {
		return errors.New("不能删除当前会话")
	}
	f.deleted = append(f.deleted, path)
	return nil
}

func (f *fakeBackend) ListModels() []ModelInfo {
	return []ModelInfo{{Ref: "p/m", Provider: "p", Model: "m", Tools: true, Current: f.model == ""}, {Ref: "p/n", Provider: "p", Model: "n"}}
}

func (f *fakeBackend) SwitchModel(ref string) (string, error) {
	switch ref {
	case "p/n":
		f.model = ref
		return "已切换模型：p/m → p/n", nil
	case "p/broken":
		return "", errors.New("缺少 API Key")
	}
	return "", NotFoundError{Err: errors.New("引用的 provider 不存在")}
}

func (f *fakeBackend) Efforts() EffortList {
	return EffortList{Current: f.effort, Levels: []EffortLevel{{Name: "auto"}, {Name: "high", Detail: "reasoning_effort: high"}}}
}

func (f *fakeBackend) SetEffort(name string) (string, error) {
	if name != "auto" && name != "high" {
		return "", NotFoundError{Err: errors.New("没有 " + name + " 档位")}
	}
	f.effort = name
	return "思考程度：auto → " + name, nil
}

// Compact 按 compact 决定行为：block 等到取消，empty 没有可压缩的，fail 摘要失败，其余成功。
func (f *fakeBackend) Compact(ctx context.Context, focus string, sink event.Sink) error {
	switch f.compact {
	case "block":
		<-ctx.Done()
		return ctx.Err()
	case "empty":
		return agent.ErrNothingToCompact
	case "fail":
		sink.Emit(event.Compacted{Mode: event.CompactSummary, Err: "模型超时", Manual: true})
		return errors.New("模型超时")
	}
	sink.Emit(event.Compacted{Mode: event.CompactSummary, TokensBefore: 100, TokensAfter: 20, Dropped: 4, Summary: "摘要:" + focus, Manual: true})
	return nil
}

// harness 把 Server 跑在一对管道上，模拟客户端逐行收发。
type harness struct {
	t     *testing.T
	in    *io.PipeWriter
	lines chan map[string]any
	done  chan error
	srv   *Server
}

func start(t *testing.T, b Backend) *harness {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	h := &harness{t: t, in: inW, lines: make(chan map[string]any, 100), done: make(chan error, 1)}
	go func() {
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				t.Errorf("输出不是合法 JSON: %q", sc.Text())
				continue
			}
			h.lines <- m
		}
	}()
	h.srv = NewServer(b, outW, nil)
	go func() {
		h.done <- h.srv.Serve(context.Background(), inR, time.Second)
		outW.Close()
	}()
	t.Cleanup(func() { inW.Close() })
	return h
}

func (h *harness) send(line string) {
	h.t.Helper()
	if _, err := io.WriteString(h.in, line+"\n"); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) next() map[string]any {
	h.t.Helper()
	select {
	case m := <-h.lines:
		return m
	case <-time.After(5 * time.Second):
		h.t.Fatal("等待服务端输出超时")
		return nil
	}
}

func errCode(m map[string]any) int {
	e, _ := m["error"].(map[string]any)
	if e == nil {
		return 0
	}
	return int(e["code"].(float64))
}

func TestInitializeReportsInfoAndPreparedNotices(t *testing.T) {
	h := start(t, &fakeBackend{prepared: []string{"MCP 服务端 x 连接失败"}})
	h.send(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	m := h.next()
	r := m["result"].(map[string]any)
	if m["id"].(float64) != 1 || r["protocolVersion"].(float64) != ProtocolVersion || r["model"] != "p/m" || r["available"] != true {
		t.Fatalf("initialize 结果不对: %v", m)
	}
	if n := r["notices"].([]any); len(n) != 1 || !strings.Contains(n[0].(string), "连接失败") {
		t.Errorf("Prepare 的提示应进入 notices: %v", r["notices"])
	}
}

func TestTurnStreamsEventsAfterResponseAndFinishesIdle(t *testing.T) {
	b := &fakeBackend{}
	h := start(t, b)
	h.send(`{"jsonrpc":"2.0","id":"a","method":"turn/start","params":{"text":"你好"}}`)
	resp := h.next()
	if resp["id"] != "a" || resp["result"].(map[string]any)["turn"].(float64) != 1 {
		t.Fatalf("应先收到 {turn:1} 响应: %v", resp)
	}
	var kinds []string
	var tool map[string]any
	for {
		m := h.next()
		if m["method"] != "event" {
			t.Fatalf("期望 event 通知: %v", m)
		}
		p := m["params"].(map[string]any)
		if p["turn"].(float64) != 1 {
			t.Errorf("turn 编号不对: %v", p)
		}
		kinds = append(kinds, p["kind"].(string))
		if p["kind"] == "tool.finished" {
			tool = p
		}
		if p["kind"] == "turn.finished" {
			break
		}
	}
	if got := strings.Join(kinds, ","); got != "turn.started,message.delta,message.completed,tool.finished,turn.finished" {
		t.Errorf("事件顺序不对: %s", got)
	}
	if tool["durationMs"].(float64) != 1500 || tool["stdout"] != "<a&b>" {
		t.Errorf("tool.finished 字段不对: %v", tool)
	}
	// 收到 turn.finished 后立刻开下一轮不能被当成 busy。
	h.send(`{"jsonrpc":"2.0","id":2,"method":"turn/start","params":{"text":"再来"}}`)
	if m := h.next(); errCode(m) != 0 || m["result"].(map[string]any)["turn"].(float64) != 2 {
		t.Fatalf("第二轮应能直接开始: %v", m)
	}
}

func TestBusyCancelAndEOFShutdown(t *testing.T) {
	h := start(t, &fakeBackend{block: true})
	h.send(`{"jsonrpc":"2.0","id":1,"method":"turn/start","params":{"text":"慢"}}`)
	h.next() // 响应
	h.next() // turn.started
	h.send(`{"jsonrpc":"2.0","id":2,"method":"turn/start","params":{"text":"插队"}}`)
	if m := h.next(); errCode(m) != CodeBusy {
		t.Fatalf("进行中再 turn/start 应返回 busy: %v", m)
	}
	h.send(`{"jsonrpc":"2.0","id":3,"method":"session/new"}`)
	if m := h.next(); errCode(m) != CodeBusy {
		t.Fatalf("进行中 session/new 应返回 busy: %v", m)
	}
	h.send(`{"jsonrpc":"2.0","id":4,"method":"turn/cancel"}`)
	got := map[string]map[string]any{}
	for range 2 { // 响应和 turn.finished 来自不同 goroutine，先后不定
		m := h.next()
		if m["id"] != nil {
			got["resp"] = m
		} else {
			got["event"] = m["params"].(map[string]any)
		}
	}
	if got["resp"]["result"].(map[string]any)["cancelled"] != true || got["event"]["reason"] != "cancelled" {
		t.Fatalf("取消结果不对: %v", got)
	}

	// 输入结束时取消进行中的一轮并退出。
	h.send(`{"jsonrpc":"2.0","id":5,"method":"turn/start","params":{"text":"又慢"}}`)
	h.next()
	h.next()
	h.in.Close()
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatalf("EOF 应正常退出: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("EOF 后没有退出")
	}
}

func TestProtocolErrors(t *testing.T) {
	h := start(t, &fakeBackend{})
	cases := []struct {
		line string
		code int
	}{
		{`{不是 json`, CodeParseError},
		{`[{"jsonrpc":"2.0","id":1,"method":"initialize"}]`, CodeInvalidRequest},
		{`{"jsonrpc":"1.0","id":1,"method":"initialize"}`, CodeInvalidRequest},
		{`{"jsonrpc":"2.0","id":{},"method":"initialize"}`, CodeInvalidRequest},
		{`{"jsonrpc":"2.0","id":1,"method":"nope"}`, CodeMethodNotFound},
		{`{"jsonrpc":"2.0","id":1,"method":"turn/start","params":{"text":"  "}}`, CodeInvalidParams},
		{`{"jsonrpc":"2.0","id":1,"method":"turn/start","params":{"text":1}}`, CodeInvalidParams},
		{`{"jsonrpc":"2.0","id":1,"method":"turn/start","params":{"images":[{"path":"不存在.png"}]}}`, CodeInvalidParams},
	}
	for _, c := range cases {
		h.send(c.line)
		if m := h.next(); errCode(m) != c.code {
			t.Errorf("%s: 期望错误码 %d，得到 %v", c.line, c.code, m)
		}
	}
	// 通知（没有 id）不回响应，下一条请求的响应应紧接着到来。
	h.send(`{"jsonrpc":"2.0","method":"nope"}`)
	h.send(`{"jsonrpc":"2.0","id":9,"method":"shutdown"}`)
	if m := h.next(); m["id"].(float64) != 9 || m["result"] == nil {
		t.Fatalf("shutdown 应返回空结果: %v", m)
	}
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown 后没有退出")
	}
}

func TestUnavailableBackend(t *testing.T) {
	h := start(t, &fakeBackend{disabled: "缺少 API Key"})
	h.send(`{"jsonrpc":"2.0","id":1,"method":"turn/start","params":{"text":"你好"}}`)
	m := h.next()
	if errCode(m) != CodeUnavailable || !strings.Contains(m["error"].(map[string]any)["message"].(string), "API Key") {
		t.Fatalf("不可用时应返回 -32002 和原因: %v", m)
	}
	h.send(`{"jsonrpc":"2.0","id":2,"method":"session/new"}`)
	if m := h.next(); errCode(m) != CodeUnavailable {
		t.Fatalf("不可用时 session/new 也应返回 -32002: %v", m)
	}
}

func TestSessionListRenameDelete(t *testing.T) {
	b := &fakeBackend{}
	h := start(t, b)
	h.send(`{"jsonrpc":"2.0","id":1,"method":"session/list"}`)
	if s := h.next()["result"].(map[string]any)["sessions"].([]any); len(s) != 0 {
		t.Fatalf("没有会话时应返回空数组: %v", s)
	}
	b.sessions = []SessionInfo{{ID: "abc", Path: "/s/abc.jsonl", Turns: 2, Current: true}}
	h.send(`{"jsonrpc":"2.0","id":2,"method":"session/list","params":{"all":true}}`)
	s := h.next()["result"].(map[string]any)["sessions"].([]any)
	if first := s[0].(map[string]any); len(s) != 2 || first["id"] != "abc" || first["turns"].(float64) != 2 || first["current"] != true {
		t.Fatalf("all 列表不对: %v", s)
	}

	h.send(`{"jsonrpc":"2.0","id":3,"method":"session/rename","params":{"path":"/s/abc.jsonl","name":"重构"}}`)
	if m := h.next(); errCode(m) != 0 || b.renamed != "/s/abc.jsonl=重构" {
		t.Fatalf("重命名不对: %v %q", m, b.renamed)
	}
	h.send(`{"jsonrpc":"2.0","id":4,"method":"session/rename","params":{"name":"x"}}`)
	if m := h.next(); errCode(m) != CodeInvalidParams {
		t.Fatalf("缺 path 应返回 -32602: %v", m)
	}
	h.send(`{"jsonrpc":"2.0","id":5,"method":"session/delete","params":{"path":"/s/current.jsonl"}}`)
	if m := h.next(); errCode(m) != CodeSession {
		t.Fatalf("删除当前会话应返回 -32003: %v", m)
	}
	h.send(`{"jsonrpc":"2.0","id":6,"method":"session/delete","params":{"path":"/s/old.jsonl"}}`)
	if m := h.next(); errCode(m) != 0 || len(b.deleted) != 1 {
		t.Fatalf("删除不对: %v %v", m, b.deleted)
	}
}

func TestSessionResumeReplaysEventsWithoutTurn(t *testing.T) {
	h := start(t, &fakeBackend{})
	h.send(`{"jsonrpc":"2.0","id":1,"method":"session/resume","params":{"selector":"9"}}`)
	if m := h.next(); errCode(m) != CodeSession {
		t.Fatalf("找不到会话应返回 -32003: %v", m)
	}
	h.send(`{"jsonrpc":"2.0","id":2,"method":"session/resume","params":{"selector":"1"}}`)
	r := h.next()["result"].(map[string]any)
	evs := r["events"].([]any)
	if r["id"] != "abc" || r["turns"].(float64) != 1 || len(evs) != 3 {
		t.Fatalf("恢复结果不对: %v", r)
	}
	for _, e := range evs {
		if _, ok := e.(map[string]any)["turn"]; ok {
			t.Errorf("重放事件不应带 turn: %v", e)
		}
	}
	if e := evs[1].(map[string]any); e["kind"] != "message.completed" || e["content"] != "旧回答" {
		t.Errorf("重放事件内容不对: %v", e)
	}
}

func TestSessionBranch(t *testing.T) {
	b := &fakeBackend{}
	h := start(t, b)
	h.send(`{"jsonrpc":"2.0","id":1,"method":"session/branch","params":{"turns":-1}}`)
	if m := h.next(); errCode(m) != CodeInvalidParams {
		t.Fatalf("负数轮数应返回 -32602: %v", m)
	}
	h.send(`{"jsonrpc":"2.0","id":2,"method":"session/branch","params":{"turns":5}}`)
	if m := h.next(); errCode(m) != CodeSession {
		t.Fatalf("轮数超出应返回 -32003: %v", m)
	}
	h.send(`{"jsonrpc":"2.0","id":3,"method":"session/branch"}`)
	r := h.next()["result"].(map[string]any)
	evs := r["events"].([]any)
	if r["id"] != "new" || r["path"] != "/s/new.jsonl" || len(evs) != 2 || len(b.branched) != 1 || b.branched[0] != 0 {
		t.Fatalf("分支结果不对: %v branched=%v", r, b.branched)
	}
	for _, e := range evs {
		if _, ok := e.(map[string]any)["turn"]; ok {
			t.Errorf("重放事件不应带 turn: %v", e)
		}
	}
}

func TestSessionBranchBusyAndUnavailable(t *testing.T) {
	h := start(t, &fakeBackend{block: true})
	h.send(`{"jsonrpc":"2.0","id":1,"method":"turn/start","params":{"text":"慢"}}`)
	h.next()
	h.next()
	h.send(`{"jsonrpc":"2.0","id":2,"method":"session/branch"}`)
	if m := h.next(); errCode(m) != CodeBusy {
		t.Fatalf("进行中 session/branch 应返回 busy: %v", m)
	}

	h2 := start(t, &fakeBackend{disabled: "缺少 API Key"})
	h2.send(`{"jsonrpc":"2.0","id":1,"method":"session/branch"}`)
	if m := h2.next(); errCode(m) != CodeUnavailable {
		t.Fatalf("不可用时 session/branch 应返回 -32002: %v", m)
	}
}

func TestCompactResults(t *testing.T) {
	b := &fakeBackend{}
	h := start(t, b)
	h.send(`{"jsonrpc":"2.0","id":1,"method":"session/compact","params":{"focus":" 接口 "}}`)
	r := h.next()["result"].(map[string]any)
	if r["tokensBefore"].(float64) != 100 || r["tokensAfter"].(float64) != 20 || r["dropped"].(float64) != 4 || r["summary"] != "摘要:接口" {
		t.Fatalf("压缩结果不对: %v", r)
	}
	for _, c := range []struct {
		mode string
		code int
	}{{"empty", CodeCompactFailed}, {"fail", CodeCompactFailed}} {
		b.compact = c.mode
		h.send(`{"jsonrpc":"2.0","id":2,"method":"session/compact"}`)
		if m := h.next(); errCode(m) != c.code {
			t.Errorf("%s: 期望 %d，得到 %v", c.mode, c.code, m)
		}
	}
}

func TestCompactIsBusyAndCancellable(t *testing.T) {
	h := start(t, &fakeBackend{compact: "block"})
	h.send(`{"jsonrpc":"2.0","id":1,"method":"session/compact"}`)
	// 压缩期间不能开新一轮；用一个立即返回的请求确认 compact 已经进入忙碌状态。
	h.send(`{"jsonrpc":"2.0","id":2,"method":"turn/start","params":{"text":"插队"}}`)
	if m := h.next(); m["id"].(float64) != 2 || errCode(m) != CodeBusy {
		t.Fatalf("压缩中 turn/start 应返回 busy: %v", m)
	}
	h.send(`{"jsonrpc":"2.0","id":3,"method":"session/resume","params":{"selector":"1"}}`)
	if m := h.next(); errCode(m) != CodeBusy {
		t.Fatalf("压缩中 session/resume 应返回 busy: %v", m)
	}
	h.send(`{"jsonrpc":"2.0","id":4,"method":"turn/cancel"}`)
	got := map[float64]map[string]any{}
	for range 2 {
		m := h.next()
		got[m["id"].(float64)] = m
	}
	if got[4]["result"].(map[string]any)["cancelled"] != true || errCode(got[1]) != CodeCancelled {
		t.Fatalf("取消压缩的结果不对: %v", got)
	}
	h.send(`{"jsonrpc":"2.0","id":5,"method":"turn/start","params":{"text":"继续"}}`)
	if m := h.next(); errCode(m) != 0 {
		t.Fatalf("取消后应恢复空闲: %v", m)
	}
}

func TestModelListAndSwitch(t *testing.T) {
	h := start(t, &fakeBackend{})
	h.send(`{"jsonrpc":"2.0","id":1,"method":"model/list"}`)
	ms := h.next()["result"].(map[string]any)["models"].([]any)
	if first := ms[0].(map[string]any); len(ms) != 2 || first["ref"] != "p/m" || first["current"] != true || first["tools"] != true {
		t.Fatalf("model/list 不对: %v", ms)
	}
	h.send(`{"jsonrpc":"2.0","id":2,"method":"model/switch","params":{"ref":"p/n"}}`)
	if r := h.next()["result"].(map[string]any); r["model"] != "p/n" || !strings.Contains(r["notice"].(string), "p/n") {
		t.Fatalf("model/switch 结果不对: %v", r)
	}
	for _, c := range []struct {
		line string
		code int
	}{
		{`{"jsonrpc":"2.0","id":3,"method":"model/switch","params":{"ref":"x/y"}}`, CodeNotFound},
		{`{"jsonrpc":"2.0","id":4,"method":"model/switch","params":{"ref":"p/broken"}}`, CodeInternal},
		{`{"jsonrpc":"2.0","id":5,"method":"model/switch"}`, CodeInvalidParams},
	} {
		h.send(c.line)
		if m := h.next(); errCode(m) != c.code {
			t.Errorf("%s: 期望 %d，得到 %v", c.line, c.code, m)
		}
	}
}

func TestEffortListAndSet(t *testing.T) {
	h := start(t, &fakeBackend{effort: "auto"})
	h.send(`{"jsonrpc":"2.0","id":1,"method":"effort/list"}`)
	r := h.next()["result"].(map[string]any)
	if r["current"] != "auto" || len(r["levels"].([]any)) != 2 {
		t.Fatalf("effort/list 不对: %v", r)
	}
	h.send(`{"jsonrpc":"2.0","id":2,"method":"effort/set","params":{"name":" HIGH "}}`)
	if r := h.next()["result"].(map[string]any); r["effort"] != "high" {
		t.Fatalf("effort/set 应规范化名字并返回新档位: %v", r)
	}
	h.send(`{"jsonrpc":"2.0","id":3,"method":"effort/set","params":{"name":"max"}}`)
	if m := h.next(); errCode(m) != CodeNotFound {
		t.Fatalf("不存在的档位应返回 -32004: %v", m)
	}
}

// 一轮进行中：effort/set 可以调（下一次请求生效），model/switch 要重建 agent，必须拒绝。
func TestEffortSetAllowedWhileBusyButSwitchIsNot(t *testing.T) {
	h := start(t, &fakeBackend{block: true})
	h.send(`{"jsonrpc":"2.0","id":1,"method":"turn/start","params":{"text":"慢"}}`)
	h.next()
	h.next()
	h.send(`{"jsonrpc":"2.0","id":2,"method":"model/switch","params":{"ref":"p/n"}}`)
	if m := h.next(); errCode(m) != CodeBusy {
		t.Fatalf("进行中 model/switch 应返回 busy: %v", m)
	}
	h.send(`{"jsonrpc":"2.0","id":3,"method":"effort/set","params":{"name":"high"}}`)
	if m := h.next(); errCode(m) != 0 {
		t.Fatalf("进行中 effort/set 应成功: %v", m)
	}
}
