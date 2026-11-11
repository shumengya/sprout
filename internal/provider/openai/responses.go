package openai

import (
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/shumengya/sprout/internal/agent"
)

// responsesProvider 实现 POST {baseUrl}/responses 的流式调用。
type responsesProvider struct{ *client }

// Stream 实现 agent.Provider。
func (p *responsesProvider) Stream(ctx context.Context, req agent.Request, emit func(agent.Chunk) error) (agent.Response, error) {
	resp, err := p.post(ctx, "/responses", buildResponsesRequest(req), req.Thinking.Extra)
	if err != nil {
		return agent.Response{}, err
	}
	defer resp.Body.Close()

	var (
		sse   = newSSEReader(resp.Body)
		st    = newRespStream(emit)
		final *responsesDone
	)
	for final == nil {
		ev, err := sse.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return agent.Response{}, readErr(ctx, err)
		}
		data := strings.TrimSpace(ev.Data)
		if data == "" || data == "[DONE]" {
			continue
		}
		var e responsesEvent
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			return agent.Response{}, &agent.ProviderError{Message: "无法解析响应流: " + clipText(data, 200), Err: err}
		}
		if final, err = st.handle(e, resp.StatusCode); err != nil {
			return agent.Response{}, err
		}
	}

	if ctx.Err() != nil {
		return agent.Response{}, ctx.Err()
	}
	// 没有收到 completed / incomplete：流被截断，不能把半截内容当成完整回答。
	if final == nil {
		return agent.Response{}, interrupted(nil, "响应流在结束前中断")
	}

	calls := st.toolCalls()
	stop := agent.StopStop
	switch {
	case final.Status == "incomplete" && final.IncompleteDetails != nil && final.IncompleteDetails.Reason == "max_output_tokens":
		stop = agent.StopLength
	case len(calls) > 0:
		stop = agent.StopToolCalls
	}
	var usage agent.Usage
	if u := final.Usage; u != nil {
		usage = agent.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, TotalTokens: u.TotalTokens}
		if d := u.OutputTokensDetails; d != nil {
			usage.ReasoningTokens = d.ReasoningTokens
		}
	}
	return agent.Response{
		// Reasoning 只用于展示：Responses 的思考条目需要加密内容才能回传，store:false 下不回传。
		Message: agent.Message{Role: agent.RoleAssistant, Content: []agent.Block{agent.TextBlock(st.text.String())},
			Reasoning: st.reasoning.String(), ToolCalls: calls},
		Usage:      usage,
		StopReason: stop,
	}, nil
}
