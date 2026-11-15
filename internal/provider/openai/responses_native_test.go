package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/config"
)

func TestChatMaxTokensParamSelectsTheField(t *testing.T) {
	// o 系列、GPT-5 在 Chat 协议下只认 max_completion_tokens，发 max_tokens 会被拒绝。
	cases := map[string]struct{ want, absent string }{
		"":                      {"max_tokens", "max_completion_tokens"},
		"max_tokens":            {"max_tokens", "max_completion_tokens"},
		"max_completion_tokens": {"max_completion_tokens", "max_tokens"},
	}
	for param, c := range cases {
		req := userReq("hi")
		req.MaxTokensParam = param
		b := buildChatRequest(req)
		data, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		if m[c.want] != float64(77) {
			t.Errorf("maxTokensParam=%q 时应发 %s=77: %v", param, c.want, m[c.want])
		}
		if _, ok := m[c.absent]; ok {
			t.Errorf("maxTokensParam=%q 时不应发 %s: %v", param, c.absent, m)
		}
	}
}

func TestResponsesRequestsEncryptedReasoningOnlyWhenThinking(t *testing.T) {
	req := userReq("hi")
	if b := buildResponsesRequest(req); len(b.Include) != 0 {
		t.Errorf("没开思考时不该请求 reasoning.encrypted_content: %v", b.Include)
	}
	req.Thinking = agent.Thinking{Effort: "high"}
	b := buildResponsesRequest(req)
	if len(b.Include) != 1 || b.Include[0] != "reasoning.encrypted_content" {
		t.Errorf("开思考时应请求 encrypted_content: %v", b.Include)
	}
}

func TestResponsesReplaysReasoningItemsBeforeTheAssistantTurn(t *testing.T) {
	// store:false 下多轮工具调用要靠回传 reasoning 条目（含加密内容），且必须排在同一条 assistant 的
	// function_call 之前，否则服务端报“缺少配对 reasoning”。
	native := &agent.Native{Protocol: config.ProtocolResponses, Model: "m",
		Data: json.RawMessage(`[{"type":"reasoning","id":"rs_1","encrypted_content":"abc"}]`)}
	req := agent.Request{Model: "m", MaxOutputTokens: 10, Messages: []agent.Message{
		{Role: agent.RoleUser, Content: []agent.Block{agent.TextBlock("hi")}},
		{Role: agent.RoleAssistant, Reasoning: "想", Native: native,
			ToolCalls: []agent.ToolCall{{ID: "c1", Name: "read", Arguments: `{"path":"a"}`}}},
		{Role: agent.RoleTool, ToolID: "c1", Content: []agent.Block{agent.TextBlock("结果")}},
	}}
	b := buildResponsesRequest(req)
	if len(b.Input) != 4 {
		t.Fatalf("input 条目数 = %d，期望 4（user / reasoning / function_call / output）", len(b.Input))
	}
	item, ok := b.Input[1].(map[string]any)
	if !ok {
		data, _ := json.Marshal(b.Input[1])
		if err := json.Unmarshal(data, &item); err != nil {
			t.Fatalf("第二条不是 reasoning 条目: %s", data)
		}
	}
	if item["type"] != "reasoning" || item["encrypted_content"] != "abc" {
		t.Errorf("应原样回传 reasoning 条目: %v", item)
	}
	if fc, ok := b.Input[2].(responsesFunctionCall); !ok || fc.CallID != "c1" {
		t.Errorf("reasoning 之后应是 function_call: %+v", b.Input[2])
	}
}

func TestResponsesIgnoresNativeFromAnotherModel(t *testing.T) {
	native := &agent.Native{Protocol: config.ProtocolResponses, Model: "other",
		Data: json.RawMessage(`[{"type":"reasoning","encrypted_content":"abc"}]`)}
	req := agent.Request{Model: "m", Messages: []agent.Message{
		{Role: agent.RoleAssistant, Content: []agent.Block{agent.TextBlock("答")}, Native: native},
	}}
	for _, it := range buildResponsesRequest(req).Input {
		if data, _ := json.Marshal(it); strings.Contains(string(data), "encrypted_content") {
			t.Errorf("换模型后不该回传旧模型的思考条目: %s", data)
		}
	}
}

func TestResponsesCollectsReasoningItemsFromTheStream(t *testing.T) {
	// response.completed 的 output 里带 reasoning 条目时，要存进 Native 供下一轮回传。
	srv := newServer(t, okBody(sse(
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"想"}`,
		`{"type":"response.completed","response":{"status":"completed","output":[`+
			`{"type":"reasoning","id":"rs_1","encrypted_content":"enc","summary":[{"type":"summary_text","text":"想"}]},`+
			`{"type":"message","content":[{"type":"output_text","text":"答"}]}]}}`,
	)))
	p := newProvider(t, config.ProtocolResponses, srv)
	resp, _, err := collect(p, userReq("hi"))
	if err != nil {
		t.Fatal(err)
	}
	n := resp.Message.Native
	if n == nil || n.Protocol != config.ProtocolResponses || n.Model != "m" {
		t.Fatalf("Native = %+v", n)
	}
	if !strings.Contains(string(n.Data), `"encrypted_content":"enc"`) {
		t.Errorf("Native.Data 应保留加密内容: %s", n.Data)
	}
	if strings.Contains(string(n.Data), `"type":"message"`) {
		t.Errorf("只该保存 reasoning 条目: %s", n.Data)
	}
}

func TestResponsesWithoutReasoningItemsHasNoNative(t *testing.T) {
	srv := newServer(t, okBody(sse(
		`{"type":"response.output_text.delta","output_index":0,"delta":"答"}`,
		`{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"答"}]}]}}`,
	)))
	resp, _, err := collect(newProvider(t, config.ProtocolResponses, srv), userReq("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Native != nil {
		t.Errorf("没有 reasoning 条目时不该生成 Native: %s", resp.Message.Native.Data)
	}
}
