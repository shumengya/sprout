package google

import (
	"encoding/json"
	"testing"

	"github.com/shumengya/sprout/internal/agent"
)

func intp(n int) *int { return &n }

// Gemini 3 用 thinkingLevel，2.5 用 thinkingBudget（0 关闭、-1 动态）；auto 不发 thinkingConfig。
// 预算为 0 时不要求返回思考摘要。
func TestThinkingConfig(t *testing.T) {
	for _, c := range []struct {
		name string
		th   agent.Thinking
		want string // generationConfig.thinkingConfig 的紧凑 JSON；空表示不应出现
	}{
		{"auto", agent.Thinking{Level: "auto"}, ""},
		{"level", agent.Thinking{Effort: "high", Summary: true}, `{"includeThoughts":true,"thinkingLevel":"high"}`},
		{"budget", agent.Thinking{Budget: intp(8192), Summary: true}, `{"includeThoughts":true,"thinkingBudget":8192}`},
		{"dynamic", agent.Thinking{Budget: intp(-1)}, `{"thinkingBudget":-1}`},
		{"off", agent.Thinking{Budget: intp(0), Summary: true}, `{"thinkingBudget":0}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := newServer(t, 200, sse(`{"candidates":[{"content":{"parts":[{"text":"好"}]},"finishReason":"STOP"}]}`))
			req := userReq("hi")
			req.Thinking = c.th
			if _, _, _, err := collect(t, newProvider(t, srv), req); err != nil {
				t.Fatal(err)
			}
			gc := asMap(srv.reqs[0].json(t)["generationConfig"])
			tc, has := gc["thinkingConfig"]
			if c.want == "" {
				if has {
					t.Errorf("不该发送 thinkingConfig，得到 %v", tc)
				}
				return
			}
			got, _ := json.Marshal(tc)
			if string(got) != c.want {
				t.Errorf("thinkingConfig = %s，想要 %s", got, c.want)
			}
			if gc["maxOutputTokens"] != float64(77) {
				t.Errorf("maxOutputTokens 丢失: %v", gc)
			}
		})
	}
}
