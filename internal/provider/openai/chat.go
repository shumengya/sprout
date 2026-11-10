package openai

import (
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/shumengya/sprout/internal/agent"
)

// chatProvider 实现 POST {baseUrl}/chat/completions 的流式调用。
type chatProvider struct{ *client }

// Stream 实现 agent.Provider。
func (p *chatProvider) Stream(ctx context.Context, req agent.Request, emit func(agent.Chunk) error) (agent.Response, error) {
	resp, err := p.post(ctx, "/chat/completions", buildChatRequest(req))
	if err != nil {
		return agent.Response{}, err
	}
	defer resp.Body.Close()

	var (
		sse       = newSSEReader(resp.Body)
		text      strings.Builder
		reasoning strings.Builder
		acc       = newChatAccum()
		finish    string
		usage     agent.Usage
		done      bool
	)
	for !done {
		ev, err := sse.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return agent.Response{}, readErr(ctx, err)
		}
		data := strings.TrimSpace(ev.Data)
		switch data {
		case "":
			continue
		case "[DONE]":
			done = true
			continue
		}

		var ch chatChunk
		if err := json.Unmarshal([]byte(data), &ch); err != nil {
			return agent.Response{}, &agent.ProviderError{Message: "无法解析响应流: " + clipText(data, 200), Err: err}
		}
		if msg, code := decodeErrorValue(ch.Error); msg != "" {
			return agent.Response{}, streamError(resp.StatusCode, code, msg)
		}
		if u := ch.Usage; u != nil {
			usage = agent.Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens, TotalTokens: u.TotalTokens}
			if d := u.CompletionTokensDetails; d != nil {
				usage.ReasoningTokens = d.ReasoningTokens
			}
		}
		if len(ch.Choices) == 0 { // include_usage 的末尾 chunk 没有 choices
			continue
		}
		c := ch.Choices[0]
		if c.Delta.ReasoningContent != "" {
			reasoning.WriteString(c.Delta.ReasoningContent)
			if err := emit(agent.Chunk{ReasoningDelta: c.Delta.ReasoningContent}); err != nil {
				return agent.Response{}, err
			}
		}
		if c.Delta.Content != "" {
			text.WriteString(c.Delta.Content)
			if err := emit(agent.Chunk{TextDelta: c.Delta.Content}); err != nil {
				return agent.Response{}, err
			}
		}
		for _, tc := range c.Delta.ToolCalls {
			acc.add(tc.Index, tc.ID, tc.Function.Name, tc.Function.Arguments)
		}
		if c.FinishReason != "" {
			finish = c.FinishReason
		}
	}

	if ctx.Err() != nil {
		return agent.Response{}, ctx.Err()
	}
	// 既没有 [DONE] 也没有 finish_reason：流被中途截断，不能把半截内容当成完整回答。
	if !done && finish == "" {
		return agent.Response{}, interrupted(nil, "响应流在结束前中断")
	}

	calls := acc.calls()
	stop := agent.StopStop
	switch {
	case finish == "length":
		stop = agent.StopLength
	case len(calls) > 0:
		stop = agent.StopToolCalls
	}
	return agent.Response{
		Message: agent.Message{
			Role: agent.RoleAssistant, Content: text.String(), Reasoning: reasoning.String(), ToolCalls: calls,
		},
		Usage:      usage,
		StopReason: stop,
	}, nil
}

// chatAccum 把流式 delta 按 index 拼成完整的 tool call。
type chatAccum struct {
	slots   []*accCall
	byIndex map[int]*accCall
}

type accCall struct {
	id, name string
	args     strings.Builder
}

func newChatAccum() *chatAccum { return &chatAccum{byIndex: map[int]*accCall{}} }

func (a *chatAccum) add(index int, id, name, args string) {
	s := a.byIndex[index]
	// 同一个 index 出现了不同的非空 id，说明是新的调用：
	// 个别兼容服务会给每个调用都填 index 0，按 index 合并会把它们拼成一个。
	if s != nil && id != "" && s.id != "" && id != s.id {
		s = nil
	}
	if s == nil {
		s = &accCall{}
		a.byIndex[index] = s
		a.slots = append(a.slots, s)
	}
	if s.id == "" {
		s.id = id
	}
	// 函数名通常只在第一个 chunk 出现一次；有些服务每个 chunk 都重复，所以只取第一次。
	if s.name == "" {
		s.name = name
	}
	s.args.WriteString(args)
}

// calls 返回拼好的调用。名字和参数都为空的槽位是无意义的残留，丢弃；
// 只有名字为空而有参数的保留，交给 agent 报“未知工具”让模型自己纠正。
func (a *chatAccum) calls() []agent.ToolCall {
	var out []agent.ToolCall
	for _, s := range a.slots {
		if s.name == "" && s.args.Len() == 0 {
			continue
		}
		id := s.id
		if id == "" {
			id = newCallID()
		}
		out = append(out, agent.ToolCall{ID: id, Name: s.name, Arguments: s.args.String()})
	}
	return out
}
