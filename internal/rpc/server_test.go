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
}

func (f *fakeBackend) Info() Info {
	return Info{Model: "p/m", Tools: []string{"bash"}, Available: f.disabled == "", Disabled: f.disabled}
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

// harness 把 Server 跑在一对管道上，模拟客户端逐行收发。
type harness struct {
	t     *testing.T
	in    *io.PipeWriter
	lines chan map[string]any
	done  chan error
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
	go func() {
		h.done <- NewServer(b, outW, nil).Serve(context.Background(), inR, time.Second)
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
