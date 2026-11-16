package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// mrtrRequest 是无状态测试服务端解出的一次请求。
type mrtrRequest struct {
	ID     json.RawMessage
	Method string
	Params map[string]json.RawMessage
}

// mrtrServer 模拟 2026-07-28 的无状态服务端：握手后的方法交给 handle 处理，
// 同时记录每次请求，供测试检查重发时带了什么。
type mrtrServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []mrtrRequest
	caps     map[string]any // 客户端在 _meta 里声明的能力
	ids      []string
}

func newMRTRServer(t *testing.T, serverCaps map[string]any, handle func(req mrtrRequest) any) *mrtrServer {
	t.Helper()
	s := &mrtrServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw struct {
			ID     json.RawMessage            `json:"id"`
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			return
		}
		req := mrtrRequest{ID: raw.ID, Method: raw.Method, Params: raw.Params}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "server/discover":
			var meta map[string]json.RawMessage
			_ = json.Unmarshal(req.Params["_meta"], &meta)
			s.mu.Lock()
			_ = json.Unmarshal(meta["io.modelcontextprotocol/clientCapabilities"], &s.caps)
			s.mu.Unlock()
			writeResult(t, w, req.ID, map[string]any{
				"supportedVersions": []string{ProtocolStateless},
				"capabilities":      serverCaps,
				"serverInfo":        map[string]any{"name": "mrtr", "version": "1"},
			})
		case "tools/list":
			writeResult(t, w, req.ID, map[string]any{"tools": []map[string]any{{"name": "ask"}}})
		default:
			s.mu.Lock()
			s.requests = append(s.requests, req)
			s.ids = append(s.ids, string(req.ID))
			s.mu.Unlock()
			writeResult(t, w, req.ID, handle(req))
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *mrtrServer) calls() []mrtrRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]mrtrRequest(nil), s.requests...)
}

func (s *mrtrServer) clientCaps() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.caps
}

func elicitInput(key string) map[string]any {
	return map[string]any{
		"resultType": "input_required",
		"inputRequests": map[string]any{
			key: map[string]any{
				"method": "elicitation/create",
				"params": map[string]any{
					"mode":    "form",
					"message": "你的名字？",
					"requestedSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{"name": map[string]any{"type": "string"}},
					},
				},
			},
		},
		"requestState": "opaque-state-1",
	}
}

func textResult(s string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": s}}}
}

// 完整的 MRTR 往返：input_required → 向用户收集 → 带 inputResponses 与原样的 requestState 重发。
func TestMRTRElicitationRoundTrip(t *testing.T) {
	srv := newMRTRServer(t, map[string]any{"tools": map[string]any{}}, func(req mrtrRequest) any {
		if _, ok := req.Params["inputResponses"]; !ok {
			return elicitInput("q1")
		}
		var resp map[string]ElicitResult
		_ = json.Unmarshal(req.Params["inputResponses"], &resp)
		var state string
		_ = json.Unmarshal(req.Params["requestState"], &state)
		return textResult("你好 " + resp["q1"].Content["name"].(string) + " / " + resp["q1"].Action + " / " + state)
	})

	var asked ElicitRequest
	c, err := Dial(context.Background(), Spec{
		Name: "mrtr", URL: srv.URL,
		Elicit: func(_ context.Context, server string, req ElicitRequest) (ElicitResult, error) {
			asked = req
			if server != "mrtr" {
				t.Errorf("server = %q", server)
			}
			return ElicitResult{Action: ElicitAccept, Content: map[string]any{"name": "小明"}}, nil
		},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	res, err := c.Call(context.Background(), "ask", json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Content[0].Text; got != "你好 小明 / accept / opaque-state-1" {
		t.Errorf("结果 = %q", got)
	}
	if asked.Message != "你的名字？" || !strings.Contains(string(asked.Schema), `"name"`) {
		t.Errorf("交给界面的请求 = %+v", asked)
	}

	calls := srv.calls()
	if len(calls) != 2 {
		t.Fatalf("服务端应收到 2 次 tools/call，实际 %d", len(calls))
	}
	// 重发必须是新的请求 id，且保留原参数。
	if srv.ids[0] == srv.ids[1] {
		t.Errorf("重发应使用新的请求 id：%v", srv.ids)
	}
	if string(calls[1].Params["arguments"]) != `{"x":1}` || string(calls[1].Params["name"]) != `"ask"` {
		t.Errorf("重发丢了原参数：%s", calls[1].Params)
	}
	if _, ok := calls[0].Params["inputResponses"]; ok {
		t.Error("首次请求不该带 inputResponses")
	}
	if _, ok := calls[0].Params["requestState"]; ok {
		t.Error("首次请求不该带 requestState")
	}
	if _, ok := srv.clientCaps()["elicitation"]; !ok {
		t.Errorf("提供了 Elicit 就应在 _meta 里声明 elicitation：%v", srv.clientCaps())
	}
	if _, ok := srv.clientCaps()["sampling"]; ok {
		t.Errorf("没提供 Sample 不该声明 sampling：%v", srv.clientCaps())
	}
}

// 用户拒绝是正常答案，照样回给服务端，不算调用失败；拒绝时不带 content。
func TestMRTRElicitationDecline(t *testing.T) {
	srv := newMRTRServer(t, map[string]any{"tools": map[string]any{}}, func(req mrtrRequest) any {
		if _, ok := req.Params["inputResponses"]; !ok {
			return elicitInput("q")
		}
		return textResult(string(req.Params["inputResponses"]))
	})
	c, err := Dial(context.Background(), Spec{
		Name: "mrtr", URL: srv.URL,
		Elicit: func(context.Context, string, ElicitRequest) (ElicitResult, error) {
			return ElicitResult{Action: ElicitDecline, Content: map[string]any{"leak": "不该发出去"}}, nil
		},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	res, err := c.Call(context.Background(), "ask", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Content[0].Text; got != `{"q":{"action":"decline"}}` {
		t.Errorf("inputResponses = %s", got)
	}
}

// 服务端没给 requestState 时，重发不能自己编一个。
func TestMRTRWithoutRequestState(t *testing.T) {
	srv := newMRTRServer(t, map[string]any{"tools": map[string]any{}}, func(req mrtrRequest) any {
		if _, ok := req.Params["inputResponses"]; !ok {
			r := elicitInput("q")
			delete(r, "requestState")
			return r
		}
		_, has := req.Params["requestState"]
		if has {
			return textResult("带了 requestState")
		}
		return textResult("ok")
	})
	c, err := Dial(context.Background(), Spec{Name: "mrtr", URL: srv.URL,
		Elicit: func(context.Context, string, ElicitRequest) (ElicitResult, error) {
			return ElicitResult{Action: ElicitAccept}, nil
		}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	res, err := c.Call(context.Background(), "ask", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content[0].Text != "ok" {
		t.Errorf("结果 = %q", res.Content[0].Text)
	}
}
