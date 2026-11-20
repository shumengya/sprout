package google

import (
	"errors"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent"
)

func TestStreamsTextThoughtAndUsage(t *testing.T) {
	srv := newServer(t, 200, sse(
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"想","thought":true}]}}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"你"},{"text":"好"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"。"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":4,"totalTokenCount":15,"thoughtsTokenCount":1}}`,
	))
	resp, text, reasoning, err := collect(t, newProvider(t, srv), userReq("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(text, "|") != "你|好|。" || strings.Join(reasoning, "|") != "想" {
		t.Errorf("增量 text=%q reasoning=%q", text, reasoning)
	}
	if resp.Message.Text() != "你好。" || resp.Message.Reasoning != "想" {
		t.Errorf("消息 = %+v", resp.Message)
	}
	if resp.StopReason != agent.StopStop {
		t.Errorf("停止原因 = %q", resp.StopReason)
	}
	// candidatesTokenCount 不含思考 token：输出要加上 thoughtsTokenCount（4 + 1），合计才对得上 total。
	if resp.Usage != (agent.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15, ReasoningTokens: 1}) {
		t.Errorf("Usage = %+v", resp.Usage)
	}
}

func TestRequestShape(t *testing.T) {
	srv := newServer(t, 200, sse(`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`))
	req := userReq("q")
	req.Tools = []agent.ToolSpec{{Name: "bash", Description: "run", Parameters: []byte(`{"type":"object"}`)}}
	if _, _, _, err := collect(t, newProvider(t, srv), req); err != nil {
		t.Fatal(err)
	}

	got := srv.reqs[0]
	if got.Path != "/models/gemini-2.5-pro:streamGenerateContent?alt=sse" {
		t.Errorf("路径 = %s", got.Path)
	}
	if got.Header.Get("x-goog-api-key") != "g-key" || got.Header.Get("Authorization") != "" {
		t.Errorf("请求头 = %v", got.Header)
	}
	b := got.json(t)
	if si := asMap(b["systemInstruction"]); asList(si["parts"])[0].(map[string]any)["text"] != "SYS" {
		t.Errorf("systemInstruction = %v", b["systemInstruction"])
	}
	if gc := asMap(b["generationConfig"]); gc["maxOutputTokens"] != float64(77) {
		t.Errorf("generationConfig = %v", b["generationConfig"])
	}
	contents := asList(b["contents"])
	if len(contents) != 1 || asMap(contents[0])["role"] != "user" {
		t.Errorf("contents = %v", contents)
	}
	decls := asList(asList(asList(b["tools"])[0].(map[string]any)["functionDeclarations"]))
	if len(decls) != 1 || asMap(decls[0])["name"] != "bash" {
		t.Errorf("tools = %v", b["tools"])
	}
}

func TestFunctionCallStreamsAsToolCall(t *testing.T) {
	srv := newServer(t, 200, sse(
		`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"bash","args":{"command":"ls"}}}]},"finishReason":"STOP"}]}`,
	))
	resp, _, _, err := collect(t, newProvider(t, srv), userReq("q"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != agent.StopToolCalls || len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("响应 = %+v", resp)
	}
	c := resp.Message.ToolCalls[0]
	if c.Name != "bash" || c.Arguments != `{"command":"ls"}` || !strings.HasPrefix(c.ID, "call_") || !strings.HasSuffix(c.ID, "_bash") {
		t.Errorf("调用 = %+v", c)
	}
}

func TestFunctionCallWithoutArgsGetsEmptyObject(t *testing.T) {
	srv := newServer(t, 200, sse(
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"noop"}}]},"finishReason":"STOP"}]}`,
	))
	resp, _, _, err := collect(t, newProvider(t, srv), userReq("q"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.ToolCalls[0].Arguments != "{}" {
		t.Errorf("参数 = %q", resp.Message.ToolCalls[0].Arguments)
	}
}

func TestHistoryMapping(t *testing.T) {
	srv := newServer(t, 200, sse(`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`))
	req := agent.Request{Model: "m", Messages: []agent.Message{
		{Role: agent.RoleUser, Content: []agent.Block{agent.TextBlock("问")}},
		{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{{ID: "c1", Name: "bash", Arguments: `{"command":"ls"}`}}},
		{Role: agent.RoleTool, ToolID: "c1", Content: []agent.Block{agent.TextBlock("a.txt")}},
		{Role: agent.RoleTool, ToolID: "c2", Content: []agent.Block{agent.TextBlock("孤儿结果")}},
		{Role: agent.RoleUser, Content: []agent.Block{agent.TextBlock("继续")}},
	}}
	if _, _, _, err := collect(t, newProvider(t, srv), req); err != nil {
		t.Fatal(err)
	}
	contents := asList(srv.reqs[0].json(t)["contents"])
	if len(contents) != 4 {
		t.Fatalf("contents 数 = %d: %v", len(contents), contents)
	}
	if asMap(contents[1])["role"] != "model" {
		t.Errorf("assistant 应映射为 model: %v", contents[1])
	}
	fc := asMap(asList(asMap(contents[1])["parts"])[0])["functionCall"]
	if asMap(fc)["name"] != "bash" {
		t.Errorf("functionCall = %v", fc)
	}
	// 两条工具结果合并到同一条 user 消息，且 functionResponse 的 name 由历史反查得到。
	if asMap(contents[2])["role"] != "user" {
		t.Fatalf("工具结果应是 user 消息: %v", contents[2])
	}
	parts := asList(asMap(contents[2])["parts"])
	if len(parts) != 2 {
		t.Fatalf("两条工具结果应合并，得到 %v", parts)
	}
	fr := asMap(asMap(parts[0])["functionResponse"])
	if fr["name"] != "bash" || !strings.Contains(string(srv.reqs[0].Body), "a.txt") {
		t.Errorf("functionResponse = %v", fr)
	}
	if resp := asMap(fr["response"]); resp["result"] != "a.txt" {
		t.Errorf("response 必须是含 result 的对象: %v", resp)
	}
}

func TestMaxTokensOmittedWhenZero(t *testing.T) {
	srv := newServer(t, 200, sse(`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`))
	req := userReq("q")
	req.MaxOutputTokens = 0
	if _, _, _, err := collect(t, newProvider(t, srv), req); err != nil {
		t.Fatal(err)
	}
	if _, ok := srv.reqs[0].json(t)["generationConfig"]; ok {
		t.Error("maxOutput 为 0 时不应发送 generationConfig")
	}
}

func TestMaxTokensFinishReason(t *testing.T) {
	srv := newServer(t, 200, sse(`{"candidates":[{"content":{"parts":[{"text":"半"}]},"finishReason":"MAX_TOKENS"}]}`))
	resp, _, _, err := collect(t, newProvider(t, srv), userReq("q"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != agent.StopLength {
		t.Errorf("停止原因 = %q", resp.StopReason)
	}
}

func TestBlockedPrompt(t *testing.T) {
	srv := newServer(t, 200, sse(`{"promptFeedback":{"blockReason":"SAFETY","blockReasonMessage":"危险内容"}}`))
	_, _, _, err := collect(t, newProvider(t, srv), userReq("q"))
	var pe *agent.ProviderError
	if !errors.As(err, &pe) || pe.Code != "content_blocked" || !strings.Contains(err.Error(), "危险内容") {
		t.Fatalf("得到 %v", err)
	}
}

func TestTruncatedStreamIsError(t *testing.T) {
	_, _, _, err := collect(t, newProvider(t, newServer(t, 200, "")), userReq("q"))
	var pe *agent.ProviderError
	if !errors.As(err, &pe) || !pe.Retryable {
		t.Fatalf("空流应报可重试错误，得到 %v", err)
	}
}

func TestStreamAndHTTPErrors(t *testing.T) {
	srv := newServer(t, 200, sse(`{"error":{"code":500,"message":"internal","status":"INTERNAL"}}`))
	_, _, _, err := collect(t, newProvider(t, srv), userReq("q"))
	var pe *agent.ProviderError
	if !errors.As(err, &pe) || pe.Code != "INTERNAL" {
		t.Fatalf("流内错误得到 %v", err)
	}

	srv2 := newServer(t, 400, `{"error":{"code":400,"message":"API key not valid","status":"INVALID_ARGUMENT"}}`)
	_, _, _, err = collect(t, newProvider(t, srv2), userReq("q"))
	if !errors.As(err, &pe) || pe.Status != 400 || pe.Code != "INVALID_ARGUMENT" || pe.Retryable {
		t.Fatalf("HTTP 错误得到 %+v", pe)
	}
}

func TestStreamPathModelPrefix(t *testing.T) {
	cases := map[string]string{
		"gemini-2.5-pro":        "/models/gemini-2.5-pro:streamGenerateContent?alt=sse",
		"models/gemini-2.5-pro": "/models/gemini-2.5-pro:streamGenerateContent?alt=sse",
	}
	for model, want := range cases {
		if got := streamPath(model); got != want {
			t.Errorf("streamPath(%q) = %q", model, got)
		}
	}
}

func TestEmptyAPIKey(t *testing.T) {
	if _, err := New(Config{BaseURL: "https://x", APIKey: ""}); err == nil {
		t.Fatal("空 API Key 应报错")
	}
}

func TestUsageParsesCachedContentTokens(t *testing.T) {
	// cachedContentTokenCount 已含在 promptTokenCount 里。
	srv := newServer(t, 200, sse(
		`{"candidates":[{"content":{"parts":[{"text":"好"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":70,"candidatesTokenCount":4,"totalTokenCount":104}}`,
	))
	resp, _, _, err := collect(t, newProvider(t, srv), userReq("hi"))
	if err != nil {
		t.Fatal(err)
	}
	want := agent.Usage{InputTokens: 100, OutputTokens: 4, TotalTokens: 104, CacheReadTokens: 70}
	if resp.Usage != want {
		t.Errorf("Usage = %+v, want %+v", resp.Usage, want)
	}
}
