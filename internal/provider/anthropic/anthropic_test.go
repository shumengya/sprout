package anthropic

import (
	"errors"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent"
)

// const/换行便于阅读：事件用 name\x00data 表示。
func TestStreamsTextReasoningAndUsage(t *testing.T) {
	srv := newServer(t, 200, sse(t,
		"message_start\x00"+`{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1}}}`,
		"content_block_start\x00"+`{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
		"content_block_delta\x00"+`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"想"}}`,
		"content_block_start\x00"+`{"type":"content_block_start","index":1,"content_block":{"type":"text"}}`,
		"content_block_delta\x00"+`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"你"}}`,
		"content_block_delta\x00"+`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"好"}}`,
		"content_block_stop\x00"+`{"type":"content_block_stop","index":1}`,
		"message_delta\x00"+`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
		"message_stop\x00"+`{"type":"message_stop"}`,
	))
	resp, text, reasoning, err := collect(t, newProvider(t, srv), userReq("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(text, "") != "你好" || strings.Join(reasoning, "") != "想" {
		t.Errorf("增量 text=%q reasoning=%q", text, reasoning)
	}
	if resp.Message.Text() != "你好" || resp.Message.Reasoning != "想" || resp.Message.Role != agent.RoleAssistant {
		t.Errorf("消息 = %+v", resp.Message)
	}
	if resp.StopReason != agent.StopStop || len(resp.Message.ToolCalls) != 0 {
		t.Errorf("停止原因 = %q calls=%v", resp.StopReason, resp.Message.ToolCalls)
	}
	if resp.Usage != (agent.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}) {
		t.Errorf("Usage = %+v", resp.Usage)
	}
}

func TestRequestShape(t *testing.T) {
	srv := newServer(t, 200, sse(t,
		"message_delta\x00"+`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
		"message_stop\x00"+`{"type":"message_stop"}`,
	))
	req := userReq("q")
	req.Tools = []agent.ToolSpec{{Name: "bash", Description: "run", Parameters: []byte(`{"type":"object"}`)}}
	if _, _, _, err := collect(t, newProvider(t, srv), req); err != nil {
		t.Fatal(err)
	}

	got := srv.reqs[0]
	if got.Path != "/messages" {
		t.Errorf("路径 = %s", got.Path)
	}
	if got.Header.Get("x-api-key") != "sk-ant" || got.Header.Get("anthropic-version") != APIVersion {
		t.Errorf("请求头 = %v", got.Header)
	}
	if got.Header.Get("Authorization") != "" {
		t.Error("不应发送 Bearer 鉴权")
	}
	b := got.json(t)
	if b["model"] != "claude-x" || b["stream"] != true || b["max_tokens"] != float64(77) || b["system"] != "SYS" {
		t.Errorf("基础字段 = %v", b)
	}
	msgs := asList(b["messages"])
	if len(msgs) != 1 {
		t.Fatalf("messages = %v", msgs)
	}
	// system 不能混进 messages。
	for _, m := range msgs {
		if asMap(m)["role"] == "system" {
			t.Error("system 不应出现在 messages 里")
		}
	}
	tools := asList(b["tools"])
	if len(tools) != 1 || asMap(tools[0])["name"] != "bash" || asMap(tools[0])["input_schema"] == nil {
		t.Errorf("tools = %v", tools)
	}
}

func TestToolUseStreaming(t *testing.T) {
	srv := newServer(t, 200, sse(t,
		"message_start\x00"+`{"type":"message_start","message":{"usage":{"input_tokens":3}}}`,
		"content_block_start\x00"+`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"bash"}}`,
		"content_block_delta\x00"+`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":"}}`,
		"content_block_delta\x00"+`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"ls\"}"}}`,
		"content_block_stop\x00"+`{"type":"content_block_stop","index":0}`,
		"message_delta\x00"+`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`,
		"message_stop\x00"+`{"type":"message_stop"}`,
	))
	resp, _, _, err := collect(t, newProvider(t, srv), userReq("q"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != agent.StopToolCalls {
		t.Errorf("停止原因 = %q", resp.StopReason)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("calls = %+v", resp.Message.ToolCalls)
	}
	c := resp.Message.ToolCalls[0]
	if c.ID != "toolu_1" || c.Name != "bash" || c.Arguments != `{"command":"ls"}` {
		t.Errorf("调用 = %+v", c)
	}
}

func TestHistoryMapping(t *testing.T) {
	srv := newServer(t, 200, sse(t,
		"message_delta\x00"+`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		"message_stop\x00"+`{"type":"message_stop"}`,
	))
	req := agent.Request{
		Model: "m",
		Messages: []agent.Message{
			{Role: agent.RoleUser, Content: []agent.Block{agent.TextBlock("问")}},
			{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{{ID: "t1", Name: "bash", Arguments: `{"command":"ls"}`}}},
			{Role: agent.RoleTool, ToolID: "t1", Content: []agent.Block{agent.TextBlock("a.txt")}},
			{Role: agent.RoleTool, ToolID: "t2", Content: []agent.Block{agent.TextBlock("孤儿结果")}},
			{Role: agent.RoleUser, Content: []agent.Block{agent.TextBlock("继续")}},
		},
	}
	if _, _, _, err := collect(t, newProvider(t, srv), req); err != nil {
		t.Fatal(err)
	}
	msgs := asList(srv.reqs[0].json(t)["messages"])
	if len(msgs) != 5 {
		t.Fatalf("messages 数 = %d: %v", len(msgs), msgs)
	}
	// assistant 的 tool_use
	blocks := asList(asMap(msgs[1])["content"])
	if len(blocks) != 1 || asMap(blocks[0])["type"] != "tool_use" || asMap(blocks[0])["id"] != "t1" {
		t.Errorf("assistant blocks = %v", blocks)
	}
	// 配对的 tool_result 在 user 消息里
	if asMap(msgs[2])["role"] != "user" {
		t.Errorf("tool_result 应是 user 消息: %v", msgs[2])
	}
	if b := asList(asMap(msgs[2])["content"])[0]; asMap(b)["type"] != "tool_result" || asMap(b)["tool_use_id"] != "t1" {
		t.Errorf("tool_result block = %v", b)
	}
	// 没有配对的孤儿结果降级为文本，不能作为 tool_result 发出去
	if b := asList(asMap(msgs[3])["content"])[0]; asMap(b)["type"] != "text" {
		t.Errorf("孤儿工具结果应降级为 text: %v", b)
	}
}

func TestConsecutiveToolResultsMergeIntoOneUserMessage(t *testing.T) {
	srv := newServer(t, 200, sse(t,
		"message_delta\x00"+`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		"message_stop\x00"+`{"type":"message_stop"}`,
	))
	req := agent.Request{Model: "m", Messages: []agent.Message{
		{Role: agent.RoleUser, Content: []agent.Block{agent.TextBlock("q")}},
		{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{
			{ID: "a", Name: "bash", Arguments: "{}"}, {ID: "b", Name: "powershell", Arguments: "{}"},
		}},
		{Role: agent.RoleTool, ToolID: "a", Content: []agent.Block{agent.TextBlock("ra")}},
		{Role: agent.RoleTool, ToolID: "b", Content: []agent.Block{agent.TextBlock("rb")}},
	}}
	if _, _, _, err := collect(t, newProvider(t, srv), req); err != nil {
		t.Fatal(err)
	}
	msgs := asList(srv.reqs[0].json(t)["messages"])
	if len(msgs) != 3 {
		t.Fatalf("两条工具结果应合并进同一条 user 消息，得到 %d 条", len(msgs))
	}
	if blocks := asList(asMap(msgs[2])["content"]); len(blocks) != 2 {
		t.Errorf("合并后的 blocks = %v", blocks)
	}
}

func TestDefaultMaxTokens(t *testing.T) {
	srv := newServer(t, 200, sse(t,
		"message_delta\x00"+`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		"message_stop\x00"+`{"type":"message_stop"}`,
	))
	req := userReq("q")
	req.MaxOutputTokens = 0
	if _, _, _, err := collect(t, newProvider(t, srv), req); err != nil {
		t.Fatal(err)
	}
	if got := srv.reqs[0].json(t)["max_tokens"]; got != float64(defaultMaxTokens) {
		t.Errorf("max_tokens = %v，应使用默认值", got)
	}
}

func TestStopReasons(t *testing.T) {
	cases := map[string]string{"end_turn": agent.StopStop, "max_tokens": agent.StopLength, "stop_sequence": agent.StopStop}
	for reason, want := range cases {
		srv := newServer(t, 200, sse(t,
			"message_delta\x00"+`{"type":"message_delta","delta":{"stop_reason":"`+reason+`"}}`,
			"message_stop\x00"+`{"type":"message_stop"}`,
		))
		resp, _, _, err := collect(t, newProvider(t, srv), userReq("q"))
		if err != nil {
			t.Fatal(err)
		}
		if resp.StopReason != want {
			t.Errorf("%s → %q，期望 %q", reason, resp.StopReason, want)
		}
	}
}

func TestTruncatedStreamIsError(t *testing.T) {
	srv := newServer(t, 200, sse(t,
		"content_block_delta\x00"+`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"半截"}}`,
	))
	_, _, _, err := collect(t, newProvider(t, srv), userReq("q"))
	var pe *agent.ProviderError
	if !errors.As(err, &pe) || !pe.Retryable {
		t.Fatalf("没有 message_stop 应报可重试错误，得到 %v", err)
	}
}

func TestStreamError(t *testing.T) {
	srv := newServer(t, 200, sse(t,
		"error\x00"+`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
	))
	_, _, _, err := collect(t, newProvider(t, srv), userReq("q"))
	var pe *agent.ProviderError
	if !errors.As(err, &pe) || pe.Code != "overloaded_error" || pe.Message != "Overloaded" {
		t.Fatalf("得到 %v", err)
	}
}

func TestHTTPErrorBody(t *testing.T) {
	srv := newServer(t, 529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	_, _, _, err := collect(t, newProvider(t, srv), userReq("q"))
	var pe *agent.ProviderError
	if !errors.As(err, &pe) || pe.Status != 529 || !pe.Retryable || pe.Code != "overloaded_error" {
		t.Fatalf("得到 %+v", pe)
	}
	if !strings.Contains(err.Error(), "Overloaded") {
		t.Errorf("错误文本 = %q", err.Error())
	}
}

func TestContextTooLong(t *testing.T) {
	srv := newServer(t, 400, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 250000 tokens > 200000 maximum"}}`)
	_, _, _, err := collect(t, newProvider(t, srv), userReq("q"))
	var pe *agent.ProviderError
	if !errors.As(err, &pe) || !pe.ContextTooLong() {
		t.Fatalf("应识别为上下文超长: %v", err)
	}
}

func TestEmptyAPIKey(t *testing.T) {
	if _, err := New(Config{BaseURL: "https://x", APIKey: "  "}); err == nil {
		t.Fatal("空 API Key 应报错")
	}
}
