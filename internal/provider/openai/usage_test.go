package openai

import (
	"testing"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/config"
)

func TestChatParsesReasoningTokens(t *testing.T) {
	// OpenAI 与 DeepSeek 都把思维链 token 放在 completion_tokens_details.reasoning_tokens。
	srv := newServer(t, okBody(sse(
		chatDelta("答"),
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":50,"total_tokens":61,`+
			`"completion_tokens_details":{"reasoning_tokens":42}}}`,
		"[DONE]",
	)))
	resp, _, err := collect(newProvider(t, config.ProtocolChat, srv), userReq("hi"))
	if err != nil {
		t.Fatal(err)
	}
	want := agent.Usage{InputTokens: 11, OutputTokens: 50, TotalTokens: 61, ReasoningTokens: 42}
	if resp.Usage != want {
		t.Errorf("Usage = %+v, want %+v", resp.Usage, want)
	}
}

func TestResponsesParsesReasoningTokens(t *testing.T) {
	srv := newServer(t, okBody(sse(
		respDelta(0, "答"),
		`{"type":"response.completed","response":{"status":"completed","output":[],`+
			`"usage":{"input_tokens":9,"output_tokens":30,"total_tokens":39,"output_tokens_details":{"reasoning_tokens":25}}}}`,
	)))
	resp, _, err := collect(newProvider(t, config.ProtocolResponses, srv), userReq("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.ReasoningTokens != 25 || resp.Usage.OutputTokens != 30 {
		t.Errorf("Usage = %+v", resp.Usage)
	}
}

func TestMissingReasoningTokensIsZero(t *testing.T) {
	// 不报告思维链 token 的服务（大多数兼容服务）：字段缺省，不能报错，也不能编造数字。
	srv := newServer(t, okBody(sse(chatDelta("答"), `{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`, "[DONE]")))
	resp, _, err := collect(newProvider(t, config.ProtocolChat, srv), userReq("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.ReasoningTokens != 0 {
		t.Errorf("ReasoningTokens = %d", resp.Usage.ReasoningTokens)
	}
}

func TestChatParsesCacheHitTokens(t *testing.T) {
	// OpenAI 放在 prompt_tokens_details.cached_tokens，DeepSeek 放在 prompt_cache_hit_tokens；
	// 都已经含在 prompt_tokens 里，InputTokens 保持全部输入的口径，命中缓存只是其中的一部分。
	cases := map[string]string{
		"OpenAI":   `"prompt_tokens_details":{"cached_tokens":80}`,
		"DeepSeek": `"prompt_cache_hit_tokens":80,"prompt_cache_miss_tokens":20`,
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			srv := newServer(t, okBody(sse(
				chatDelta("答"),
				`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105,`+extra+`}}`,
				"[DONE]",
			)))
			resp, _, err := collect(newProvider(t, config.ProtocolChat, srv), userReq("hi"))
			if err != nil {
				t.Fatal(err)
			}
			want := agent.Usage{InputTokens: 100, OutputTokens: 5, TotalTokens: 105, CacheReadTokens: 80}
			if resp.Usage != want {
				t.Errorf("Usage = %+v, want %+v", resp.Usage, want)
			}
		})
	}
}

func TestResponsesParsesCachedTokens(t *testing.T) {
	srv := newServer(t, okBody(sse(
		respDelta(0, "答"),
		`{"type":"response.completed","response":{"status":"completed","output":[],`+
			`"usage":{"input_tokens":100,"output_tokens":5,"total_tokens":105,"input_tokens_details":{"cached_tokens":64}}}}`,
	)))
	resp, _, err := collect(newProvider(t, config.ProtocolResponses, srv), userReq("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.InputTokens != 100 || resp.Usage.CacheReadTokens != 64 || resp.Usage.CacheWriteTokens != 0 {
		t.Errorf("Usage = %+v", resp.Usage)
	}
}
