package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 旧版协议：服务端在 tools/call 的 SSE 流里发一条真正的 elicitation/create 请求，
// 客户端要用单独的 POST 回复，服务端收到后才发最终结果。
func legacyAskServer(t *testing.T, serverReq map[string]any, replies chan map[string]json.RawMessage) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		var all map[string]json.RawMessage
		body := new(strings.Builder)
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&all); err != nil {
			return
		}
		b, _ := json.Marshal(all)
		body.Write(b)
		_ = json.Unmarshal(b, &req)

		switch {
		case req.Method == "":
			// 客户端对服务端请求的回复。
			replies <- all
			w.WriteHeader(http.StatusAccepted)
		case req.Method == "server/discover":
			writeError(t, w, req.ID, -32601, "不支持")
		case req.Method == "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case req.Method == "initialize":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Mcp-Session-Id", "s1")
			writeResult(t, w, req.ID, map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{"tools": map[string]any{}, "prompts": map[string]any{}},
				"serverInfo":      map[string]any{"name": "legacy"},
			})
		case req.Method == "tools/list":
			w.Header().Set("Content-Type", "application/json")
			writeResult(t, w, req.ID, map[string]any{"tools": []map[string]any{{"name": "ask"}}})
		case req.Method == "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			data, _ := json.Marshal(serverReq)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
			select {
			case reply := <-replies:
				replies <- reply // 留给测试检查
				text, _ := json.Marshal(string(reply["result"]) + string(reply["error"]))
				fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"content\":[{\"type\":\"text\",\"text\":%s}]}}\n\n", req.ID, text)
			case <-time.After(5 * time.Second):
				fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%s,\"error\":{\"code\":-1,\"message\":\"客户端没回复\"}}\n\n", req.ID)
			}
		default:
			w.Header().Set("Content-Type", "application/json")
			writeError(t, w, req.ID, -32601, "不支持")
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func elicitServerRequest() map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": "srv-1", "method": "elicitation/create",
		"params": map[string]any{"message": "颜色？", "requestedSchema": map[string]any{"type": "object"}}}
}

func TestLegacyHTTPElicitation(t *testing.T) {
	replies := make(chan map[string]json.RawMessage, 2)
	srv := legacyAskServer(t, elicitServerRequest(), replies)

	var asked ElicitRequest
	c, err := Dial(context.Background(), Spec{Name: "legacy", URL: srv.URL,
		Elicit: func(_ context.Context, _ string, req ElicitRequest) (ElicitResult, error) {
			asked = req
			return ElicitResult{Action: ElicitAccept, Content: map[string]any{"color": "蓝"}}, nil
		}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	res, err := c.Call(context.Background(), "ask", nil)
	if err != nil {
		t.Fatal(err)
	}
	if asked.Message != "颜色？" {
		t.Errorf("请求 = %+v", asked)
	}
	reply := <-replies
	if string(reply["id"]) != `"srv-1"` {
		t.Errorf("回复的 id 必须与服务端请求一致：%s", reply["id"])
	}
	if !strings.Contains(res.Content[0].Text, `"action":"accept"`) || !strings.Contains(res.Content[0].Text, "蓝") {
		t.Errorf("服务端收到的回复 = %q", res.Content[0].Text)
	}
}

// 没接界面时回 -32601，服务端不会一直等。
func TestLegacyHTTPElicitationUnsupported(t *testing.T) {
	replies := make(chan map[string]json.RawMessage, 2)
	srv := legacyAskServer(t, elicitServerRequest(), replies)
	c, err := Dial(context.Background(), Spec{Name: "legacy", URL: srv.URL}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// 没有请求处理函数的路径不会走到 handler：Dial 总会注册，input 里回 -32601。
	res, err := c.Call(context.Background(), "ask", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content[0].Text, "-32601") {
		t.Errorf("应回 -32601，服务端收到 %q", res.Content[0].Text)
	}
}

// 用户慢慢填表单不能触发工具调用的超时。
func TestLegacyHTTPSlowAnswerDoesNotTimeOut(t *testing.T) {
	replies := make(chan map[string]json.RawMessage, 2)
	srv := legacyAskServer(t, elicitServerRequest(), replies)
	c, err := Dial(context.Background(), Spec{Name: "legacy", URL: srv.URL, Timeout: 400 * time.Millisecond,
		Elicit: func(context.Context, string, ElicitRequest) (ElicitResult, error) {
			time.Sleep(900 * time.Millisecond)
			return ElicitResult{Action: ElicitDecline}, nil
		}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	res, err := c.Call(context.Background(), "ask", nil)
	if err != nil {
		t.Fatalf("等用户期间不该超时：%v", err)
	}
	if !strings.Contains(res.Content[0].Text, "decline") {
		t.Errorf("结果 = %q", res.Content[0].Text)
	}
}

// 采样被用户拒绝：回 JSON-RPC 错误（-1）。
func TestLegacyHTTPSamplingRejected(t *testing.T) {
	replies := make(chan map[string]json.RawMessage, 2)
	req := map[string]any{"jsonrpc": "2.0", "id": 7, "method": "sampling/createMessage",
		"params": map[string]any{"messages": []any{map[string]any{"role": "user", "content": map[string]any{"type": "text", "text": "hi"}}}}}
	srv := legacyAskServer(t, req, replies)
	c, err := Dial(context.Background(), Spec{Name: "legacy", URL: srv.URL,
		Sample: func(context.Context, string, SampleRequest) (SampleResult, error) {
			return SampleResult{}, fmt.Errorf("用户拒绝了采样请求")
		}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	res, err := c.Call(context.Background(), "ask", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content[0].Text, "用户拒绝了采样请求") || !strings.Contains(res.Content[0].Text, `"code":-1`) {
		t.Errorf("服务端收到 %q", res.Content[0].Text)
	}
	if got := string((<-replies)["id"]); got != "7" {
		t.Errorf("数字 id 要原样回复，得到 %s", got)
	}
}

// stdio：服务端在 stdout 上发请求，客户端在不阻塞读循环的前提下回复。
func TestStdioElicitation(t *testing.T) {
	spec := stdioSpec()
	spec.Elicit = func(_ context.Context, _ string, req ElicitRequest) (ElicitResult, error) {
		if req.Message != "颜色？" {
			t.Errorf("message = %q", req.Message)
		}
		return ElicitResult{Action: ElicitAccept, Content: map[string]any{"color": "红"}}, nil
	}
	c, err := Dial(context.Background(), spec, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	res, err := c.Call(context.Background(), "ask", nil)
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, `"id":"srv-1"`) || !strings.Contains(text, `"action":"accept"`) || !strings.Contains(text, "红") {
		t.Errorf("服务端收到 %q", text)
	}
}

// initialize 声明的能力必须与接入的界面一致。
func TestStdioDeclaresCapabilities(t *testing.T) {
	check := func(spec Spec, want string, absent string) {
		t.Helper()
		c, err := Dial(context.Background(), spec, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		res, err := c.Call(context.Background(), "caps", nil)
		if err != nil {
			t.Fatal(err)
		}
		got := res.Content[0].Text
		if want != "" && !strings.Contains(got, want) {
			t.Errorf("capabilities = %s，应含 %s", got, want)
		}
		if absent != "" && strings.Contains(got, absent) {
			t.Errorf("capabilities = %s，不该含 %s", got, absent)
		}
	}
	check(stdioSpec(), "{}", "elicitation")

	spec := stdioSpec()
	spec.Elicit = func(context.Context, string, ElicitRequest) (ElicitResult, error) { return ElicitResult{}, nil }
	check(spec, "elicitation", "sampling")

	spec = stdioSpec()
	spec.Sample = func(context.Context, string, SampleRequest) (SampleResult, error) { return SampleResult{}, nil }
	check(spec, "sampling", "elicitation")
}
