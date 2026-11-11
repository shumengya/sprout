package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent"
)

func intp(n int) *int { return &n }

func doneSSE(t *testing.T) string {
	return sse(t,
		"message_start\x00"+`{"type":"message_start","message":{"usage":{"input_tokens":1}}}`,
		"message_delta\x00"+`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
		"message_stop\x00"+`{"type":"message_stop"}`,
	)
}

// 档位换算后的参数按 Anthropic 的字段写进请求：强度走 adaptive + output_config.effort，
// 预算走 enabled + budget_tokens，预算 0 是显式关闭；auto（零值）什么也不发。
func TestThinkingRequestShape(t *testing.T) {
	cases := []struct {
		name         string
		th           agent.Thinking
		wantThinking string // thinking 字段的紧凑 JSON；空表示不应出现
		wantEffort   string // output_config.effort；空表示 output_config 不应出现
		wantMax      float64
	}{
		{name: "auto", th: agent.Thinking{Level: "auto"}, wantMax: 77},
		{name: "adaptive", th: agent.Thinking{Effort: "high"}, wantThinking: `{"type":"adaptive"}`, wantEffort: "high", wantMax: 77},
		{name: "adaptive 摘要", th: agent.Thinking{Effort: "max", Summary: true}, wantThinking: `{"display":"summarized","type":"adaptive"}`, wantEffort: "max", wantMax: 77},
		// 预算大于 maxOutput 时抬高 max_tokens，满足 budget_tokens < max_tokens。
		{name: "预算", th: agent.Thinking{Budget: intp(2048)}, wantThinking: `{"budget_tokens":2048,"type":"enabled"}`, wantMax: 2048 + answerReserve},
		{name: "关闭", th: agent.Thinking{Budget: intp(0)}, wantThinking: `{"type":"disabled"}`, wantMax: 77},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newServer(t, 200, doneSSE(t))
			req := userReq("hi")
			req.Thinking = c.th
			if _, _, _, err := collect(t, newProvider(t, srv), req); err != nil {
				t.Fatal(err)
			}
			body := srv.reqs[0].json(t)
			th, has := body["thinking"]
			switch {
			case c.wantThinking == "" && has:
				t.Errorf("不该发送 thinking，得到 %v", th)
			case c.wantThinking != "":
				got, _ := json.Marshal(th)
				if string(got) != c.wantThinking {
					t.Errorf("thinking = %s，想要 %s", got, c.wantThinking)
				}
			}
			oc, has := body["output_config"]
			switch {
			case c.wantEffort == "" && has:
				t.Errorf("不该发送 output_config，得到 %v", oc)
			case c.wantEffort != "" && asMap(oc)["effort"] != c.wantEffort:
				t.Errorf("output_config = %v，想要 effort=%s", oc, c.wantEffort)
			}
			if body["max_tokens"] != c.wantMax {
				t.Errorf("max_tokens = %v，想要 %v", body["max_tokens"], c.wantMax)
			}
		})
	}
}

// 带签名的思考块、加密思考块和工具调用按 index 顺序保存成原始内容；同一模型下一次请求原样回传。
func TestThinkingBlocksRoundTrip(t *testing.T) {
	srv := newServer(t, 200, sse(t,
		"message_start\x00"+`{"type":"message_start","message":{"usage":{"input_tokens":3}}}`,
		"content_block_start\x00"+`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		"content_block_delta\x00"+`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"先看"}}`,
		"content_block_delta\x00"+`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"目录"}}`,
		"content_block_delta\x00"+`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"SIG-A"}}`,
		"content_block_stop\x00"+`{"type":"content_block_stop","index":0}`,
		"content_block_start\x00"+`{"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"ENC"}}`,
		"content_block_stop\x00"+`{"type":"content_block_stop","index":1}`,
		"content_block_start\x00"+`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
		"content_block_delta\x00"+`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"我看看"}}`,
		"content_block_start\x00"+`{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"toolu_1","name":"bash","input":{}}}`,
		"content_block_delta\x00"+`{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"{\"command\":"}}`,
		"content_block_delta\x00"+`{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"\"ls\"}"}}`,
		"message_delta\x00"+`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`,
		"message_stop\x00"+`{"type":"message_stop"}`,
	))
	req := userReq("列目录")
	req.Thinking = agent.Thinking{Effort: "high"}
	resp, _, reasoning, err := collect(t, newProvider(t, srv), req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(reasoning, "") != "先看目录" {
		t.Errorf("思维链增量 = %q", reasoning)
	}
	n := resp.Message.Native
	if n == nil || n.Protocol != "anthropic-messages" || n.Model != "claude-x" {
		t.Fatalf("Native = %+v", n)
	}
	want := `[{"type":"thinking","thinking":"先看目录","signature":"SIG-A"},` +
		`{"type":"redacted_thinking","data":"ENC"},` +
		`{"type":"text","text":"我看看"},` +
		`{"type":"tool_use","id":"toolu_1","name":"bash","input":{"command":"ls"}}]`
	if string(n.Data) != want {
		t.Errorf("Native.Data =\n%s\n想要\n%s", n.Data, want)
	}

	// 下一轮：同一模型原样回传，工具结果仍然配对。
	history := append(req.Messages, resp.Message, agent.Message{
		Role: agent.RoleTool, ToolID: "toolu_1", Content: []agent.Block{agent.TextBlock("a.txt")},
	})
	next := buildRequest(agent.Request{Model: "claude-x", Messages: history, Thinking: req.Thinking})
	got, _ := json.Marshal(next.Messages[1].Content)
	if string(got) != want {
		t.Errorf("回传的 assistant 内容 =\n%s\n想要\n%s", got, want)
	}
	if len(next.Messages) != 3 || !isToolResult(next.Messages[2]) {
		t.Errorf("工具结果没有跟在后面: %+v", next.Messages)
	}

	// 换了模型：思考块的签名对新模型无效，按内部消息重建，不带思考块。
	other := buildRequest(agent.Request{Model: "claude-y", Messages: history})
	got, _ = json.Marshal(other.Messages[1].Content)
	if strings.Contains(string(got), "thinking") || !strings.Contains(string(got), `"toolu_1"`) {
		t.Errorf("换模型后的 assistant 内容 = %s", got)
	}
}

// 没有思考块时不保存原始内容，会话记录与以前一样。
func TestNoNativeWithoutThinking(t *testing.T) {
	srv := newServer(t, 200, sse(t,
		"message_start\x00"+`{"type":"message_start","message":{"usage":{"input_tokens":1}}}`,
		"content_block_start\x00"+`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		"content_block_delta\x00"+`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"好"}}`,
		"message_delta\x00"+`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		"message_stop\x00"+`{"type":"message_stop"}`,
	))
	resp, _, _, err := collect(t, newProvider(t, srv), userReq("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Native != nil {
		t.Errorf("没有思考块却保存了原始内容: %s", resp.Message.Native.Data)
	}
}

// 额外字段合并进请求体顶层，可以覆盖适配器写的同名字段。
func TestThinkingExtraBody(t *testing.T) {
	srv := newServer(t, 200, doneSSE(t))
	req := userReq("hi")
	req.Thinking = agent.Thinking{Extra: map[string]json.RawMessage{"thinking": json.RawMessage(`{"type":"between_tools"}`)}}
	if _, _, _, err := collect(t, newProvider(t, srv), req); err != nil {
		t.Fatal(err)
	}
	if got := asMap(srv.reqs[0].json(t)["thinking"])["type"]; got != "between_tools" {
		t.Errorf("thinking.type = %v", got)
	}
}
