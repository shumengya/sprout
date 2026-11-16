package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// requestAsync 在后台发一条服务端请求，结果从通道取。
func requestAsync(h *harness, ctx context.Context, method string, params any) <-chan reqResult {
	ch := make(chan reqResult, 1)
	go func() {
		res, err := h.srv.Request(ctx, method, params)
		ch <- reqResult{res, err}
	}()
	return ch
}

type reqResult struct {
	res json.RawMessage
	err error
}

func (h *harness) result(ch <-chan reqResult) reqResult {
	h.t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		h.t.Fatal("等待 Request 返回超时")
		return reqResult{}
	}
}

func TestRequestRoundTrip(t *testing.T) {
	h := start(t, &fakeBackend{})
	ch := requestAsync(h, context.Background(), "mcp/elicit", map[string]any{"server": "fs"})

	req := h.next()
	if req["method"] != "mcp/elicit" || req["jsonrpc"] != "2.0" {
		t.Fatalf("请求 = %v", req)
	}
	id, _ := req["id"].(string)
	if id == "" {
		t.Fatalf("请求必须带 id：%v", req)
	}
	if p, _ := req["params"].(map[string]any); p["server"] != "fs" {
		t.Errorf("params = %v", req["params"])
	}

	// 响应夹在别的请求中间也能对上：先发一个不相关的，再答。
	h.send(`{"jsonrpc":"2.0","id":1,"method":"effort/list"}`)
	h.send(`{"jsonrpc":"2.0","id":"` + id + `","result":{"action":"accept","values":{"a":1}}}`)
	if r := h.result(ch); r.err != nil || string(r.res) != `{"action":"accept","values":{"a":1}}` {
		t.Fatalf("结果 = %s, %v", r.res, r.err)
	}
	if resp := h.next(); resp["id"].(float64) != 1 {
		t.Errorf("不相关请求的响应 = %v", resp)
	}
}

func TestRequestClientError(t *testing.T) {
	h := start(t, &fakeBackend{})
	ch := requestAsync(h, context.Background(), "mcp/elicit", nil)
	id := h.next()["id"].(string)
	h.send(`{"jsonrpc":"2.0","id":"` + id + `","error":{"code":-32601,"message":"不认识"}}`)
	r := h.result(ch)
	var e *Error
	if !errors.As(r.err, &e) || e.Code != CodeMethodNotFound || e.Message != "不认识" {
		t.Fatalf("应收到客户端的错误，得到 %v", r.err)
	}
}

func TestRequestCancelledAndIgnoresStrayResponses(t *testing.T) {
	h := start(t, &fakeBackend{})
	ctx, cancel := context.WithCancel(context.Background())
	ch := requestAsync(h, ctx, "mcp/sample", nil)
	id := h.next()["id"].(string)
	cancel()
	if r := h.result(ch); !errors.Is(r.err, context.Canceled) {
		t.Fatalf("取消应返回 context.Canceled，得到 %v", r.err)
	}
	// 取消之后才到的回答、没人问过的 id、数字 id，都被丢弃且不影响后续请求。
	h.send(`{"jsonrpc":"2.0","id":"` + id + `","result":{}}`)
	h.send(`{"jsonrpc":"2.0","id":"srv-999","result":{}}`)
	h.send(`{"jsonrpc":"2.0","id":7,"result":{}}`)
	h.send(`{"jsonrpc":"2.0","id":2,"method":"effort/list"}`)
	if resp := h.next(); resp["id"].(float64) != 2 {
		t.Errorf("后续请求应正常响应：%v", resp)
	}
}

// 同一条请求被回答两次：只认第一次。
func TestRequestDuplicateResponse(t *testing.T) {
	h := start(t, &fakeBackend{})
	ch := requestAsync(h, context.Background(), "mcp/elicit", nil)
	id := h.next()["id"].(string)
	h.send(`{"jsonrpc":"2.0","id":"` + id + `","result":{"n":1}}`)
	h.send(`{"jsonrpc":"2.0","id":"` + id + `","result":{"n":2}}`)
	if r := h.result(ch); string(r.res) != `{"n":1}` {
		t.Fatalf("应只认第一次，得到 %s", r.res)
	}
}

// 连接结束时还在等的请求要返回，不能把进行中的一轮永远挂住。
func TestRequestEndsWhenConnectionCloses(t *testing.T) {
	h := start(t, &fakeBackend{})
	ch := requestAsync(h, context.Background(), "mcp/elicit", nil)
	h.next()
	h.in.Close()
	if r := h.result(ch); !errors.Is(r.err, ErrClientGone) {
		t.Fatalf("得到 %v", r.err)
	}
}

func TestInitializeRecordsClientCapabilities(t *testing.T) {
	h := start(t, &fakeBackend{})
	if h.srv.ClientSupports(CapElicitation) {
		t.Fatal("initialize 之前不该认为客户端支持")
	}
	h.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"capabilities":{"elicitation":true,"sampling":false}}}`)
	h.next()
	if !h.srv.ClientSupports(CapElicitation) || h.srv.ClientSupports(CapSampling) {
		t.Errorf("能力 elicitation=%v sampling=%v", h.srv.ClientSupports(CapElicitation), h.srv.ClientSupports(CapSampling))
	}

	// 再次 initialize 不带能力：以最新一次为准。
	h.send(`{"jsonrpc":"2.0","id":2,"method":"initialize"}`)
	h.next()
	if h.srv.ClientSupports(CapElicitation) {
		t.Error("重新 initialize 后应以新声明为准")
	}
	// 能力字段格式不对时当作都不支持，不让 initialize 失败。
	h.send(`{"jsonrpc":"2.0","id":3,"method":"initialize","params":{"capabilities":"yes"}}`)
	if resp := h.next(); resp["error"] != nil {
		t.Errorf("initialize 不该因能力格式失败：%v", resp)
	}
}
