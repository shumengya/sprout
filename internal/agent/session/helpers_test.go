package session

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/tool"
)

// funcProvider 用一个函数按请求内容决定回应。只用于测试。
type funcProvider func(req agent.Request) agent.Response

func (f funcProvider) Stream(_ context.Context, req agent.Request, emit func(agent.Chunk) error) (agent.Response, error) {
	resp := f(req)
	if resp.Message.Reasoning != "" {
		if err := emit(agent.Chunk{ReasoningDelta: resp.Message.Reasoning}); err != nil {
			return agent.Response{}, err
		}
	}
	if text := resp.Message.Text(); text != "" {
		if err := emit(agent.Chunk{TextDelta: text}); err != nil {
			return agent.Response{}, err
		}
	}
	return resp, nil
}

// isSummaryRequest 判断请求是不是上下文摘要请求：不带工具，只有一条包含固定标题的用户消息。
func isSummaryRequest(req agent.Request) bool {
	return len(req.Tools) == 0 && len(req.Messages) == 1 &&
		strings.Contains(req.Messages[0].Text(), "需要并入摘要的对话")
}

func answer(text string) agent.Response {
	return agent.Response{Message: agent.NewTextMessage(agent.RoleAssistant, text), StopReason: agent.StopStop}
}

func calls(reasoning string, cs ...agent.ToolCall) agent.Response {
	// 不带 Usage：假的用量会让校准后的估算失真，压缩触发点就不可预测了。
	return agent.Response{Message: agent.Message{Reasoning: reasoning, ToolCalls: cs}, StopReason: agent.StopToolCalls}
}

type funcTool struct {
	name string
	run  func(ctx context.Context) (tool.Result, error)
}

func (f funcTool) Name() string                { return f.name }
func (f funcTool) Description() string         { return "d" }
func (f funcTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (f funcTool) Run(ctx context.Context, _ json.RawMessage) (tool.Result, error) {
	return f.run(ctx)
}

func registryOf(t *testing.T, tools ...tool.Tool) *tool.Registry {
	t.Helper()
	r := tool.NewRegistry()
	for _, tl := range tools {
		if err := r.Register(tl); err != nil {
			t.Fatal(err)
		}
	}
	return r
}
