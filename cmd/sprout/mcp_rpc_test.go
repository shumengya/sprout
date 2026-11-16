package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/shumengya/sprout/internal/rpc"
	"github.com/shumengya/sprout/internal/tui"
)

// stubBackend 只实现 initialize 会调用的两个方法，其余方法不会被这些测试触发。
type stubBackend struct{ rpc.Backend }

func (stubBackend) Info() rpc.Info    { return rpc.Info{} }
func (stubBackend) Prepare() []string { return nil }

// rpcPeer 是接在真实 rpc.Server 另一端的假客户端。
type rpcPeer struct {
	t     *testing.T
	srv   *rpc.Server
	in    io.WriteCloser
	lines chan map[string]any
}

func newRPCPeer(t *testing.T) *rpcPeer {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	srv := rpc.NewServer(stubBackend{}, outW, nil)
	done := make(chan struct{})
	go func() {
		_ = srv.Serve(context.Background(), inR, time.Second)
		outW.Close()
		close(done)
	}()
	p := &rpcPeer{t: t, srv: srv, in: inW, lines: make(chan map[string]any, 16)}
	go func() {
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			var m map[string]any
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				p.lines <- m
			}
		}
	}()
	t.Cleanup(func() {
		inW.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Serve 没有在输入关闭后返回")
		}
	})
	return p
}

func (p *rpcPeer) send(s string) {
	p.t.Helper()
	if _, err := io.WriteString(p.in, s+"\n"); err != nil {
		p.t.Fatal(err)
	}
}

func (p *rpcPeer) next() map[string]any {
	p.t.Helper()
	select {
	case m := <-p.lines:
		return m
	case <-time.After(5 * time.Second):
		p.t.Fatal("等待服务端输出超时")
		return nil
	}
}

// initialize 声明能力并吃掉响应。
func (p *rpcPeer) initialize(caps string) {
	p.t.Helper()
	p.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"capabilities":` + caps + `}}`)
	p.next()
}

type askResult struct {
	ans tui.AskAnswer
	err error
}

// forwardAsync 在后台走一次 rpcForward，让测试线程去扮演客户端回答。
func (p *rpcPeer) forwardAsync(a *tui.Ask) <-chan askResult {
	ch := make(chan askResult, 1)
	go func() {
		ans, err := rpcForward(p.srv)(context.Background(), a)
		ch <- askResult{ans, err}
	}()
	return ch
}

func waitAsk(t *testing.T, ch <-chan askResult) askResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("等待 rpcForward 返回超时")
		return askResult{}
	}
}

func formAsk() *tui.Ask {
	return &tui.Ask{Server: "fs", Message: "请填写", Fields: []tui.AskField{
		{Name: "env", Title: "环境", Kind: "enum", Choices: []string{"dev", "prod"}, Required: true},
		{Name: "n", Kind: "integer"},
		{Name: "ratio", Kind: "number"},
		{Name: "ok", Kind: "boolean"},
		{Name: "note", Kind: "string"},
	}}
}

// 声明了 elicitation 的客户端收到 mcp/elicit，字段原样带过去，回答被转成 schema 要求的类型。
func TestRPCForwardElicit(t *testing.T) {
	p := newRPCPeer(t)
	p.initialize(`{"elicitation":true}`)
	ch := p.forwardAsync(formAsk())

	req := p.next()
	if req["method"] != "mcp/elicit" {
		t.Fatalf("method = %v", req["method"])
	}
	params := req["params"].(map[string]any)
	if params["server"] != "fs" || params["message"] != "请填写" {
		t.Errorf("params = %v", params)
	}
	fields := params["fields"].([]any)
	if len(fields) != 5 || fields[0].(map[string]any)["name"] != "env" || fields[0].(map[string]any)["required"] != true {
		t.Errorf("fields = %v", fields)
	}
	if c := fields[0].(map[string]any)["choices"].([]any); len(c) != 2 || c[1] != "prod" {
		t.Errorf("choices = %v", c)
	}

	id := req["id"].(string)
	p.send(`{"jsonrpc":"2.0","id":"` + id + `","result":{"action":"accept","values":{"env":"prod","n":3,"ratio":1.5,"ok":true,"note":"hi"}}}`)
	r := waitAsk(t, ch)
	if r.err != nil || r.ans.Action != tui.AskAccept {
		t.Fatalf("得到 %+v", r)
	}
	want := map[string]any{"env": "prod", "n": int64(3), "ratio": 1.5, "ok": true, "note": "hi"}
	for k, v := range want {
		if r.ans.Values[k] != v {
			t.Errorf("values[%s] = %#v，想要 %#v", k, r.ans.Values[k], v)
		}
	}
}

// 非必填字段可以不填；decline 与 cancel 不带值，也不校验值。
func TestRPCForwardDeclineAndOptional(t *testing.T) {
	p := newRPCPeer(t)
	p.initialize(`{"elicitation":true}`)

	ch := p.forwardAsync(formAsk())
	id := p.next()["id"].(string)
	p.send(`{"jsonrpc":"2.0","id":"` + id + `","result":{"action":"accept","values":{"env":"dev"}}}`)
	if r := waitAsk(t, ch); r.err != nil || len(r.ans.Values) != 1 || r.ans.Values["env"] != "dev" {
		t.Errorf("只填必填项: %+v", r)
	}

	for _, action := range []string{"decline", "cancel"} {
		ch := p.forwardAsync(formAsk())
		id := p.next()["id"].(string)
		p.send(`{"jsonrpc":"2.0","id":"` + id + `","result":{"action":"` + action + `","values":{"bogus":1}}}`)
		if r := waitAsk(t, ch); r.err != nil || r.ans.Action != action || r.ans.Values != nil {
			t.Errorf("%s: %+v", action, r)
		}
	}
}

func TestRPCForwardSample(t *testing.T) {
	p := newRPCPeer(t)
	p.initialize(`{"sampling":true}`)
	ch := p.forwardAsync(&tui.Ask{Server: "fs", Kind: tui.AskKindSample, Message: "总结一下"})
	req := p.next()
	if req["method"] != "mcp/sample" {
		t.Fatalf("method = %v", req["method"])
	}
	if _, has := req["params"].(map[string]any)["fields"]; has {
		t.Error("采样确认不该带 fields")
	}
	p.send(`{"jsonrpc":"2.0","id":"` + req["id"].(string) + `","result":{"action":"accept","values":{"x":1}}}`)
	if r := waitAsk(t, ch); r.err != nil || r.ans.Action != tui.AskAccept || r.ans.Values != nil {
		t.Errorf("采样同意不带值: %+v", r)
	}
}

// 没声明对应能力就直接失败，且不能往客户端发出请求。
func TestRPCForwardRequiresCapability(t *testing.T) {
	p := newRPCPeer(t)
	p.initialize(`{"sampling":true}`)
	ch := p.forwardAsync(formAsk())
	if r := waitAsk(t, ch); r.err == nil || !strings.Contains(r.err.Error(), "elicitation") {
		t.Errorf("应因缺 elicitation 能力失败: %v", r.err)
	}
	// 反过来：只声明 elicitation 时采样被拒。
	p.initialize(`{"elicitation":true}`)
	ch = p.forwardAsync(&tui.Ask{Kind: tui.AskKindSample})
	if r := waitAsk(t, ch); r.err == nil || !strings.Contains(r.err.Error(), "sampling") {
		t.Errorf("应因缺 sampling 能力失败: %v", r.err)
	}
	// 两次都被拒绝，服务端不该向客户端发过任何请求。
	select {
	case m := <-p.lines:
		t.Errorf("没声明能力时不该收到服务端输出: %v", m)
	case <-time.After(200 * time.Millisecond):
	}
}

// 客户端回的值不合法时拒绝，不把它交给 MCP 服务端。
func TestRPCForwardRejectsBadAnswers(t *testing.T) {
	cases := []struct{ name, result, want string }{
		{"缺必填", `{"action":"accept","values":{"n":1}}`, "必填"},
		{"枚举越界", `{"action":"accept","values":{"env":"qa"}}`, "之一"},
		{"类型不对", `{"action":"accept","values":{"env":"dev","ok":"yes"}}`, "布尔"},
		{"整数带小数", `{"action":"accept","values":{"env":"dev","n":1.5}}`, "整数"},
		{"数字给了字符串", `{"action":"accept","values":{"env":"dev","ratio":"1"}}`, "数字"},
		{"字符串给了数字", `{"action":"accept","values":{"env":"dev","note":5}}`, "字符串"},
		{"多余字段", `{"action":"accept","values":{"env":"dev","zzz":1}}`, "没有的字段"},
		{"action 非法", `{"action":"maybe"}`, "action"},
		{"结果不是对象", `[1]`, "无法解析"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newRPCPeer(t)
			p.initialize(`{"elicitation":true}`)
			ch := p.forwardAsync(formAsk())
			id := p.next()["id"].(string)
			p.send(`{"jsonrpc":"2.0","id":"` + id + `","result":` + c.result + `}`)
			if r := waitAsk(t, ch); r.err == nil || !strings.Contains(r.err.Error(), c.want) {
				t.Errorf("想要包含 %q 的错误，得到 %+v", c.want, r)
			}
		})
	}
}

func TestRPCForwardClientError(t *testing.T) {
	p := newRPCPeer(t)
	p.initialize(`{"elicitation":true}`)
	ch := p.forwardAsync(formAsk())
	id := p.next()["id"].(string)
	p.send(`{"jsonrpc":"2.0","id":"` + id + `","error":{"code":-32601,"message":"不支持"}}`)
	if r := waitAsk(t, ch); r.err == nil || !strings.Contains(r.err.Error(), "不支持") {
		t.Errorf("应带上客户端的错误信息: %v", r.err)
	}
}

// 客户端断开时等待中的请求要返回，不能把 MCP 调用永远挂住。
func TestRPCForwardClientGone(t *testing.T) {
	p := newRPCPeer(t)
	p.initialize(`{"elicitation":true}`)
	ch := p.forwardAsync(formAsk())
	p.next()
	p.in.Close()
	if r := waitAsk(t, ch); r.err == nil {
		t.Error("连接关闭后应返回错误")
	}
}

// remote 模式下 forward 接上之前请求直接失败，接上之后走 forward 而不是终端通道。
func TestMCPInputRemoteForwarding(t *testing.T) {
	in := newMCPInput(true)
	if in.asks != nil {
		t.Fatal("remote 模式不该有终端通道")
	}
	if _, err := in.ask(context.Background(), &tui.Ask{}); err == nil {
		t.Error("forward 接上之前应失败")
	}
	var got *tui.Ask
	in.setForward(func(_ context.Context, a *tui.Ask) (tui.AskAnswer, error) {
		got = a
		return tui.AskAnswer{Action: tui.AskDecline}, nil
	})
	ans, err := in.ask(context.Background(), &tui.Ask{Server: "s"})
	if err != nil || ans.Action != tui.AskDecline || got == nil || got.Server != "s" {
		t.Errorf("ans=%+v err=%v got=%+v", ans, err, got)
	}
	in.close()
	if _, err := in.ask(context.Background(), &tui.Ask{}); err == nil {
		t.Error("关闭后应失败")
	}
}
