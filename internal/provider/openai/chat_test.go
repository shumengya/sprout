package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/config"
)

const chatText = `{"choices":[{"delta":{"content":"%s"}}]}`

func chatDelta(s string) string { return strings.Replace(chatText, "%s", s, 1) }

func TestChatStreamsTextAndUsage(t *testing.T) {
	srv := newServer(t, okBody(sse(
		`{"choices":[{"delta":{"role":"assistant","content":""}}]}`,
		chatDelta("你"), chatDelta("好"),
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":2,"total_tokens":13}}`,
		"[DONE]",
	)))
	resp, deltas, err := collect(newProvider(t, config.ProtocolChat, srv), userReq("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(deltas, "|") != "|你|好" && strings.Join(deltas, "|") != "你|好" {
		t.Errorf("文本增量 = %q", deltas)
	}
	if resp.Message.Content != "你好" || resp.StopReason != agent.StopStop || len(resp.Message.ToolCalls) != 0 {
		t.Errorf("响应 = %+v", resp)
	}
	if resp.Usage != (agent.Usage{InputTokens: 11, OutputTokens: 2, TotalTokens: 13}) {
		t.Errorf("Usage = %+v（usage 在 finish_reason 之后的独立 chunk 里，不能提前退出）", resp.Usage)
	}
}

func TestChatRequestShape(t *testing.T) {
	srv := newServer(t, okBody(sse(chatDelta("ok"), `{"choices":[{"delta":{},"finish_reason":"stop"}]}`, "[DONE]")))
	req := userReq("q")
	req.Tools = []agent.ToolSpec{{Name: "bash", Description: "run", Parameters: []byte(`{"type":"object"}`)}}
	if _, _, err := collect(newProvider(t, config.ProtocolChat, srv), req); err != nil {
		t.Fatal(err)
	}

	got := srv.reqs[0]
	if got.Path != "/v1/chat/completions" {
		t.Errorf("路径 = %s", got.Path)
	}
	if got.Header.Get("Authorization") != "Bearer k" || got.Header.Get("Content-Type") != "application/json" {
		t.Errorf("请求头 = %v", got.Header)
	}
	b := got.json(t)
	if b["model"] != "m" || b["stream"] != true || b["max_tokens"] != float64(77) {
		t.Errorf("基础字段 = %v", b)
	}
	if asMap(b["stream_options"])["include_usage"] != true {
		t.Errorf("应请求 include_usage: %v", b["stream_options"])
	}
	msgs := asList(b["messages"])
	if len(msgs) != 2 || asMap(msgs[0])["role"] != "system" || asMap(msgs[0])["content"] != "SYS" {
		t.Fatalf("system 应放在 messages[0]: %v", msgs)
	}
	fn := asMap(asMap(asList(b["tools"])[0])["function"])
	if asMap(asList(b["tools"])[0])["type"] != "function" || fn["name"] != "bash" || fn["description"] != "run" {
		t.Errorf("工具声明应嵌套在 function 下: %v", b["tools"])
	}
	if _, ok := b["store"]; ok {
		t.Error("Chat 请求不应带 store 字段")
	}
}

func TestChatHistoryWithToolRoundTrip(t *testing.T) {
	srv := newServer(t, okBody(sse(chatDelta("ok"), `{"choices":[{"delta":{},"finish_reason":"stop"}]}`, "[DONE]")))
	req := userReq("找入口")
	req.Messages = append(req.Messages,
		agent.Message{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{{ID: "c1", Name: "bash", Arguments: `{"command":"ls"}`}}},
		agent.Message{Role: agent.RoleTool, ToolID: "c1", Content: "exit_code: 0"},
	)
	if _, _, err := collect(newProvider(t, config.ProtocolChat, srv), req); err != nil {
		t.Fatal(err)
	}
	msgs := asList(srv.reqs[0].json(t)["messages"])
	as, tm := asMap(msgs[2]), asMap(msgs[3])
	if as["role"] != "assistant" || as["content"] != "" {
		t.Errorf("无文本的 assistant 消息 content 应为空串而不是缺失: %v", as)
	}
	tc := asMap(asList(as["tool_calls"])[0])
	if tc["id"] != "c1" || tc["type"] != "function" || asMap(tc["function"])["arguments"] != `{"command":"ls"}` {
		t.Errorf("tool_calls = %v", as["tool_calls"])
	}
	if tm["role"] != "tool" || tm["tool_call_id"] != "c1" || tm["content"] != "exit_code: 0" {
		t.Errorf("工具结果消息 = %v", tm)
	}
}

func TestChatAccumulatesParallelToolCalls(t *testing.T) {
	srv := newServer(t, okBody(sse(
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"bash","arguments":""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"powershell","arguments":""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"command\":\"dir\"}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls\"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		"[DONE]",
	)))
	resp, _, err := collect(newProvider(t, config.ProtocolChat, srv), userReq("q"))
	if err != nil {
		t.Fatal(err)
	}
	c := resp.Message.ToolCalls
	if resp.StopReason != agent.StopToolCalls || len(c) != 2 {
		t.Fatalf("响应 = %+v", resp)
	}
	if c[0] != (agent.ToolCall{ID: "call_a", Name: "bash", Arguments: `{"command":"ls"}`}) ||
		c[1] != (agent.ToolCall{ID: "call_b", Name: "powershell", Arguments: `{"command":"dir"}`}) {
		t.Errorf("tool calls = %+v", c)
	}
}

func TestChatToolCallsWithoutFinishReasonToolCalls(t *testing.T) {
	// 有 tool call 时以实际累积结果为准，不要求 finish_reason 是 tool_calls。
	srv := newServer(t, okBody(sse(
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"x","function":{"name":"bash","arguments":"{}"}}]},"finish_reason":"stop"}]}`,
		"[DONE]",
	)))
	resp, _, err := collect(newProvider(t, config.ProtocolChat, srv), userReq("q"))
	if err != nil || resp.StopReason != agent.StopToolCalls || len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("得到 %+v, %v", resp, err)
	}
}

func TestChatSameIndexDifferentIDsAreSeparateCalls(t *testing.T) {
	// 个别兼容服务给每个调用都填 index 0，只能靠 id 区分。
	srv := newServer(t, okBody(sse(
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"A","function":{"name":"bash","arguments":"{\"n\":1}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"B","function":{"name":"bash","arguments":"{\"n\":2}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`, "[DONE]",
	)))
	resp, _, err := collect(newProvider(t, config.ProtocolChat, srv), userReq("q"))
	c := resp.Message.ToolCalls
	if err != nil || len(c) != 2 || c[0].ID != "A" || c[1].ID != "B" || c[1].Arguments != `{"n":2}` {
		t.Fatalf("得到 %+v, %v", c, err)
	}
}

func TestChatGeneratesMissingCallIDs(t *testing.T) {
	srv := newServer(t, okBody(sse(
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"bash","arguments":"{}"}},{"index":1,"function":{"name":"bash","arguments":"{}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`, "[DONE]",
	)))
	resp, _, err := collect(newProvider(t, config.ProtocolChat, srv), userReq("q"))
	c := resp.Message.ToolCalls
	if err != nil || len(c) != 2 || c[0].ID == "" || c[1].ID == "" || c[0].ID == c[1].ID {
		t.Fatalf("应为缺少 id 的调用生成互不相同的 id: %+v, %v", c, err)
	}
}

func TestChatDropsEmptyToolCallSlots(t *testing.T) {
	srv := newServer(t, okBody(sse(
		`{"choices":[{"delta":{"tool_calls":[{"index":0}]}}]}`,
		`{"choices":[{"delta":{"content":"文本"},"finish_reason":"stop"}]}`, "[DONE]",
	)))
	resp, _, err := collect(newProvider(t, config.ProtocolChat, srv), userReq("q"))
	if err != nil || len(resp.Message.ToolCalls) != 0 || resp.StopReason != agent.StopStop {
		t.Fatalf("名字和参数都为空的残留槽位应丢弃: %+v, %v", resp, err)
	}
}

func TestChatReasoningStreamedAndAccumulated(t *testing.T) {
	// DeepSeek 思考模式：先流 reasoning_content，再流 content；content 为 null 的 chunk 也要能解析。
	srv := newServer(t, okBody(sse(
		`{"choices":[{"delta":{"role":"assistant","content":null,"reasoning_content":""}}]}`,
		`{"choices":[{"delta":{"content":null,"reasoning_content":"先想"}}]}`,
		`{"choices":[{"delta":{"content":null,"reasoning_content":"一下"}}]}`,
		`{"choices":[{"delta":{"content":"答案","reasoning_content":null}}]}`,
		`{"choices":[{"delta":{"content":"","reasoning_content":null},"finish_reason":"stop"}]}`, "[DONE]",
	)))
	var reasoning, text []string
	resp, err := newProvider(t, config.ProtocolChat, srv).Stream(context.Background(), userReq("q"), func(c agent.Chunk) error {
		if c.ReasoningDelta != "" {
			reasoning = append(reasoning, c.ReasoningDelta)
		}
		if c.TextDelta != "" {
			text = append(text, c.TextDelta)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(reasoning, "|") != "先想|一下" || strings.Join(text, "|") != "答案" {
		t.Errorf("增量: reasoning=%q text=%q", reasoning, text)
	}
	if resp.Message.Reasoning != "先想一下" || resp.Message.Content != "答案" {
		t.Errorf("响应 = %+v", resp.Message)
	}
}

func TestChatReplaysReasoningOnAssistantMessages(t *testing.T) {
	// 带 tools 的请求必须原样回传历史 assistant 的 reasoning_content。
	srv := newServer(t, okBody(sse(chatDelta("ok"), `{"choices":[{"delta":{},"finish_reason":"stop"}]}`, "[DONE]")))
	req := userReq("q")
	req.Messages = append(req.Messages,
		agent.Message{Role: agent.RoleAssistant, Reasoning: "我要先看目录",
			ToolCalls: []agent.ToolCall{{ID: "c1", Name: "bash", Arguments: `{}`}}},
		agent.Message{Role: agent.RoleTool, ToolID: "c1", Content: "out"},
		agent.Message{Role: agent.RoleAssistant, Content: "好了", Reasoning: "整理一下"},
	)
	if _, _, err := collect(newProvider(t, config.ProtocolChat, srv), req); err != nil {
		t.Fatal(err)
	}
	msgs := asList(srv.reqs[0].json(t)["messages"])
	if asMap(msgs[2])["reasoning_content"] != "我要先看目录" || asMap(msgs[4])["reasoning_content"] != "整理一下" {
		t.Errorf("历史 assistant 消息应带 reasoning_content: %v / %v", msgs[2], msgs[4])
	}
}

func TestChatOmitsReasoningContentWhenEmpty(t *testing.T) {
	// 没有思维链时不能多发字段：别的 OpenAI 兼容服务可能因为未知字段而拒绝请求。
	srv := newServer(t, okBody(sse(chatDelta("ok"), `{"choices":[{"delta":{},"finish_reason":"stop"}]}`, "[DONE]")))
	req := userReq("q")
	req.Messages = append(req.Messages, agent.Message{Role: agent.RoleAssistant, Content: "旧回答"},
		agent.Message{Role: agent.RoleUser, Content: "再问"})
	if _, _, err := collect(newProvider(t, config.ProtocolChat, srv), req); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(srv.reqs[0].Body), "reasoning_content") {
		t.Errorf("没有思维链时请求体不应出现 reasoning_content: %s", srv.reqs[0].Body)
	}
}

func TestChatLengthStop(t *testing.T) {
	srv := newServer(t, okBody(sse(chatDelta("被截"), `{"choices":[{"delta":{},"finish_reason":"length"}]}`, "[DONE]")))
	resp, _, err := collect(newProvider(t, config.ProtocolChat, srv), userReq("q"))
	if err != nil || resp.StopReason != agent.StopLength {
		t.Fatalf("得到 %+v, %v", resp, err)
	}
}

func TestChatAcceptsMissingDONEWhenFinished(t *testing.T) {
	srv := newServer(t, okBody(sse(chatDelta("ok"), `{"choices":[{"delta":{},"finish_reason":"stop"}]}`)))
	resp, _, err := collect(newProvider(t, config.ProtocolChat, srv), userReq("q"))
	if err != nil || resp.Message.Content != "ok" {
		t.Fatalf("已有 finish_reason 时缺少 [DONE] 应接受: %+v, %v", resp, err)
	}
}

func TestChatTruncatedStreamIsRetryableError(t *testing.T) {
	srv := newServer(t, okBody(sse(chatDelta("半"))))
	_, deltas, err := collect(newProvider(t, config.ProtocolChat, srv), userReq("q"))
	var pe *agent.ProviderError
	if !errors.As(err, &pe) || !pe.Retryable || !strings.Contains(pe.Error(), "中断") {
		t.Fatalf("被截断的流应是可重试的 ProviderError，得到 %v", err)
	}
	if len(deltas) != 1 {
		t.Errorf("已收到的增量仍应推送: %v", deltas)
	}
}

func TestChatInStreamError(t *testing.T) {
	srv := newServer(t, okBody(sse(chatDelta("a"), `{"error":{"message":"内容被拦截","code":"content_filter"}}`)))
	_, _, err := collect(newProvider(t, config.ProtocolChat, srv), userReq("q"))
	var pe *agent.ProviderError
	if !errors.As(err, &pe) || pe.Message != "内容被拦截" || pe.Code != "content_filter" {
		t.Fatalf("流内 error 应转成 ProviderError，得到 %v", err)
	}
}

func TestChatMalformedChunk(t *testing.T) {
	srv := newServer(t, okBody(sse(`{not json`)))
	_, _, err := collect(newProvider(t, config.ProtocolChat, srv), userReq("q"))
	var pe *agent.ProviderError
	if !errors.As(err, &pe) || !strings.Contains(pe.Message, "无法解析") {
		t.Fatalf("得到 %v", err)
	}
}

func TestChatEmitErrorAborts(t *testing.T) {
	srv := newServer(t, okBody(sse(chatDelta("a"), chatDelta("b"), "[DONE]")))
	stop := errors.New("stop")
	n := 0
	_, err := newProvider(t, config.ProtocolChat, srv).Stream(context.Background(), userReq("q"),
		func(agent.Chunk) error { n++; return stop })
	if !errors.Is(err, stop) || n != 1 {
		t.Fatalf("emit 返回错误应立即中止：err=%v n=%d", err, n)
	}
}

func TestChatCancelMidStream(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.Write([]byte(sse(chatDelta("a"))))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done() // 客户端取消后才返回
	}))
	defer srv.Close()
	p, _ := New(Config{Protocol: config.ProtocolChat, BaseURL: srv.URL, APIKey: "k"})

	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; time.Sleep(50 * time.Millisecond); cancel() }()
	begin := time.Now()
	_, err := p.Stream(ctx, userReq("q"), func(agent.Chunk) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应返回 context.Canceled，得到 %v", err)
	}
	if time.Since(begin) > 3*time.Second {
		t.Fatal("取消后没有及时返回")
	}
}
