package openai

import (
	"encoding/json"
	"testing"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/config"
)

const chatDone = `{"choices":[{"index":0,"delta":{"content":"好"},"finish_reason":"stop"}]}`

const respDone = `{"type":"response.completed","response":{"status":"completed","output":[]}}`

// Chat 协议只有 reasoning_effort；auto（零值）时请求里不出现任何思考字段。
func TestChatReasoningEffort(t *testing.T) {
	for _, c := range []struct {
		name string
		th   agent.Thinking
		want any // reasoning_effort 的值；nil 表示不应出现
	}{
		{"auto", agent.Thinking{Level: "auto"}, nil},
		{"high", agent.Thinking{Effort: "high", Summary: true}, "high"},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := newServer(t, okBody(sse(chatDone, "[DONE]")))
			req := userReq("hi")
			req.Thinking = c.th
			if _, _, err := collect(newProvider(t, config.ProtocolChat, srv), req); err != nil {
				t.Fatal(err)
			}
			got, has := srv.reqs[0].json(t)["reasoning_effort"]
			if c.want == nil && has || c.want != nil && got != c.want {
				t.Errorf("reasoning_effort = %v（存在=%v），想要 %v", got, has, c.want)
			}
		})
	}
}

// DeepSeek 这类服务关闭思考靠请求体里的私有开关，由档位的 body 声明合并进来。
func TestChatExtraBody(t *testing.T) {
	srv := newServer(t, okBody(sse(chatDone, "[DONE]")))
	req := userReq("hi")
	req.Thinking = agent.Thinking{Extra: map[string]json.RawMessage{"thinking": json.RawMessage(`{"type":"disabled"}`)}}
	if _, _, err := collect(newProvider(t, config.ProtocolChat, srv), req); err != nil {
		t.Fatal(err)
	}
	body := srv.reqs[0].json(t)
	if asMap(body["thinking"])["type"] != "disabled" {
		t.Errorf("thinking = %v", body["thinking"])
	}
	if _, has := body["reasoning_effort"]; has {
		t.Error("只声明了 body 时不该发送 reasoning_effort")
	}
	if body["model"] != "m" || body["stream"] != true {
		t.Errorf("合并额外字段后原有字段丢失: %v", body)
	}
}

// Responses 协议发 reasoning.effort，要摘要时加 summary=auto，并在流里接收摘要文本。
func TestResponsesReasoning(t *testing.T) {
	for _, c := range []struct {
		name string
		th   agent.Thinking
		want string // reasoning 的紧凑 JSON；空表示不应出现
	}{
		{"auto", agent.Thinking{}, ""},
		{"摘要", agent.Thinking{Effort: "high", Summary: true}, `{"effort":"high","summary":"auto"}`},
		{"无摘要", agent.Thinking{Effort: "low"}, `{"effort":"low"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := newServer(t, okBody(sse(
				`{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"想一想"}`,
				`{"type":"response.output_text.delta","output_index":1,"delta":"好"}`,
				respDone,
			)))
			req := userReq("hi")
			req.Thinking = c.th
			resp, _, err := collect(newProvider(t, config.ProtocolResponses, srv), req)
			if err != nil {
				t.Fatal(err)
			}
			r, has := srv.reqs[0].json(t)["reasoning"]
			if c.want == "" {
				if has {
					t.Errorf("不该发送 reasoning，得到 %v", r)
				}
				return
			}
			got, _ := json.Marshal(r)
			if string(got) != c.want {
				t.Errorf("reasoning = %s，想要 %s", got, c.want)
			}
			if resp.Message.Reasoning != "想一想" {
				t.Errorf("思考摘要 = %q", resp.Message.Reasoning)
			}
		})
	}
}
