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

// legacyServer 模拟只认 initialize 握手的旧版服务端（现网绝大多数）。
func legacyServer(t *testing.T, calls *[]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			if r.Method == http.MethodDelete {
				return // 客户端关闭时发的 DELETE 没有请求体
			}
			t.Errorf("请求不是合法 JSON: %v", err)
			return
		}
		*calls = append(*calls, req.Method)
		if r.Header.Get("Mcp-Session-Id") == "" && req.Method != "initialize" {
			// 旧版服务端要求 initialize 之后每次请求都带会话 id。
			if req.Method != "notifications/initialized" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		if req.Method == "server/discover" {
			writeError(t, w, req.ID, -32601, "不支持")
			return
		}
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if req.Method == "initialize" {
			w.Header().Set("Mcp-Session-Id", "sess-1")
			writeResult(t, w, req.ID, map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "fake", "version": "1.0"},
			})
			return
		}
		if r.Header.Get("MCP-Protocol-Version") != "2025-06-18" {
			t.Errorf("后续请求应带 MCP-Protocol-Version: 2025-06-18，实际 %q", r.Header.Get("MCP-Protocol-Version"))
		}
		switch req.Method {
		case "tools/list":
			writeResult(t, w, req.ID, map[string]any{"tools": []map[string]any{{
				"name":        "echo",
				"description": "回显",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
			}}})
		case "tools/call":
			var p struct {
				Name string `json:"name"`
				Args struct {
					Text string `json:"text"`
				} `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			writeResult(t, w, req.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": "echo: " + p.Args.Text}}})
		default:
			writeError(t, w, req.ID, -32601, "不支持 "+req.Method)
		}
	})
	return httptest.NewServer(mux)
}

func writeResult(t *testing.T, w http.ResponseWriter, id json.RawMessage, result any) {
	t.Helper()
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
}

func writeError(t *testing.T, w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	t.Helper()
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "error": map[string]any{"code": code, "message": msg}})
}

func TestDialLegacyHTTPAndCallTool(t *testing.T) {
	var calls []string
	srv := legacyServer(t, &calls)
	defer srv.Close()

	c, err := Dial(context.Background(), Spec{Name: "fake", URL: srv.URL + "/mcp"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// 客户端总是先试无状态的 server/discover，被拒（-32601）后回退到 initialize。
	if got := strings.Join(calls, ","); !strings.HasPrefix(got, "server/discover,initialize,notifications/initialized,tools/list") {
		t.Errorf("握手顺序不对: %s", got)
	}
	if c.Version() != "2025-06-18" {
		t.Errorf("版本 = %q", c.Version())
	}
	if c.ServerInfo().Name != "fake" {
		t.Errorf("服务端信息 = %+v", c.ServerInfo())
	}
	tools := c.Tools()
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("工具 = %+v", tools)
	}

	res, err := c.Call(context.Background(), "echo", json.RawMessage(`{"text":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "echo: hi" {
		t.Errorf("结果 = %+v", res.Content)
	}
}

// statelessServer 模拟 2026-07-28 的无状态服务端：没有 initialize，只认 server/discover。
func statelessServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Mcp-Method") == "" {
			t.Errorf("无状态协议要求带 Mcp-Method 头")
		}
		if got := r.Header.Get("MCP-Protocol-Version"); got != "" && got != ProtocolStateless {
			t.Errorf("版本头 = %q", got)
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Meta map[string]any `json:"_meta"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "server/discover":
			if req.Params.Meta["io.modelcontextprotocol/protocolVersion"] != ProtocolStateless {
				t.Errorf("_meta 缺少协议版本: %+v", req.Params.Meta)
			}
			writeResult(t, w, req.ID, map[string]any{
				"supportedVersions": []string{"2025-11-25", ProtocolStateless},
				"capabilities":      map[string]any{"tools": map[string]any{}},
				"serverInfo":        map[string]any{"name": "new", "version": "2.0"},
			})
		case "tools/list":
			writeResult(t, w, req.ID, map[string]any{"tools": []map[string]any{{"name": "ping"}}})
		default:
			writeError(t, w, req.ID, -32601, "不支持")
		}
	})
	return httptest.NewServer(mux)
}

func TestDialStatelessHTTP(t *testing.T) {
	srv := statelessServer(t)
	defer srv.Close()

	c, err := Dial(context.Background(), Spec{Name: "new", URL: srv.URL + "/mcp"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Version() != ProtocolStateless {
		t.Errorf("版本 = %q，应走无状态协议", c.Version())
	}
	if len(c.Tools()) != 1 || c.Tools()[0].Name != "ping" {
		t.Errorf("工具 = %+v", c.Tools())
	}
}

// sseServer 用 SSE 返回响应，前面先发一条无关通知，验证客户端会跳过它。
func sseServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "server/discover" {
			writeError(t, w, req.ID, -32601, "不支持")
			return
		}
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		switch req.Method {
		case "initialize":
			fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\",\"params\":{}}\n\n")
			fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"protocolVersion\":\"2025-06-18\",\"serverInfo\":{\"name\":\"sse\"}}}\n\n", req.ID)
		case "tools/list":
			fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"tools\":[{\"name\":\"t\"}]}}\n\n", req.ID)
		default:
			writeError(t, w, req.ID, -32601, "不支持")
		}
	})
	return httptest.NewServer(mux)
}

func TestDialHTTPOverSSE(t *testing.T) {
	srv := sseServer(t)
	defer srv.Close()

	c, err := Dial(context.Background(), Spec{Name: "sse", URL: srv.URL + "/mcp"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if len(c.Tools()) != 1 || c.Tools()[0].Name != "t" {
		t.Errorf("工具 = %+v", c.Tools())
	}
}

func TestDialReportsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	_, err := Dial(context.Background(), Spec{Name: "bad", URL: srv.URL, Timeout: time.Second}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("应带上 HTTP 状态码，得到 %v", err)
	}
}

func TestToolName(t *testing.T) {
	cases := map[[2]string]string{
		{"tavily", "tavily_search"}:     "mcp__tavily__tavily_search",
		{"my server", "a.b c"}:          "mcp__my_server__a_b_c",
		{"x", "9lives"}:                 "mcp__x__9lives",
		{"s", strings.Repeat("t", 100)}: "mcp__s__" + strings.Repeat("t", 64-8),
		{"", ""}:                        "mcp____",
	}
	for in, want := range cases {
		if got := ToolName(in[0], in[1]); got != want {
			t.Errorf("ToolName(%q, %q) = %q，期望 %q", in[0], in[1], got, want)
		}
	}
}
