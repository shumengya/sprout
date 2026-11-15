package openai

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/config"
)

// respStream 累积一次 Responses 流式响应的状态。
//
// 函数调用的拼装有三个来源，按可信度从低到高：
// function_call_arguments.delta（增量）→ function_call_arguments.done（该调用的完整参数）→
// output_item.done / response.completed 里的完整 item。后到的覆盖先到的，
// 这样即使服务端省略了其中某类事件，也能得到完整结果。
type respStream struct {
	emit    func(agent.Chunk) error
	text    strings.Builder
	textIdx map[int]bool // 已经通过增量输出过文本的 output_index
	calls   map[int]*respCall
	// reasoning 是思考摘要（或兼容服务给出的原始思考文本）；thinkIdx 记录已经流式输出过的 reasoning 条目。
	reasoning strings.Builder
	thinkIdx  map[int]bool
	// rawReasoning 按 output_index 保存 reasoning 条目的原文，用于下一轮原样回传（含 encrypted_content）。
	rawReasoning map[int]json.RawMessage
}

type respCall struct {
	callID, name string
	args         strings.Builder
}

func newRespStream(emit func(agent.Chunk) error) *respStream {
	return &respStream{emit: emit, textIdx: map[int]bool{}, calls: map[int]*respCall{}, thinkIdx: map[int]bool{},
		rawReasoning: map[int]json.RawMessage{}}
}

func (s *respStream) call(idx int) *respCall {
	c := s.calls[idx]
	if c == nil {
		c = &respCall{}
		s.calls[idx] = c
	}
	return c
}

func (s *respStream) addText(idx int, t string) error {
	if t == "" {
		return nil
	}
	s.textIdx[idx] = true
	s.text.WriteString(t)
	return s.emit(agent.Chunk{TextDelta: t})
}

func (s *respStream) addReasoning(idx int, t string) error {
	if t == "" {
		return nil
	}
	s.thinkIdx[idx] = true
	s.reasoning.WriteString(t)
	return s.emit(agent.Chunk{ReasoningDelta: t})
}

// finishItem 处理一个完整的 output item，来自 output_item.done 或 response.completed 的 output。
// 它是幂等的：同一个 item 处理多次不会重复输出文本。
func (s *respStream) finishItem(idx int, it responsesItem) error {
	switch it.Type {
	case "function_call":
		c := s.call(idx)
		if it.CallID != "" {
			c.callID = it.CallID
		}
		if it.Name != "" {
			c.name = it.Name
		}
		if it.Arguments != "" {
			c.args.Reset()
			c.args.WriteString(it.Arguments)
		}
	case "message":
		// 服务端没有发文本增量时，用完整 item 补发一次。
		if !s.textIdx[idx] {
			return s.addText(idx, it.text())
		}
	case "reasoning":
		// 原文留着下一轮回传；ack 掉重复处理（output_item.done 与 response.completed 会各来一次）。
		if len(it.Raw) > 0 {
			s.rawReasoning[idx] = it.Raw
		}
		// 没有流式输出过摘要时用完整 item 补上，多段摘要之间空一行。
		if !s.thinkIdx[idx] {
			var parts []string
			for _, p := range it.Summary {
				if p.Text != "" {
					parts = append(parts, p.Text)
				}
			}
			return s.addReasoning(idx, strings.Join(parts, "\n\n"))
		}
	}
	return nil
}

// handle 处理一个事件。遇到终态事件（completed / incomplete）返回非 nil 的 *responsesDone。
func (s *respStream) handle(e responsesEvent, httpStatus int) (*responsesDone, error) {
	switch e.Type {
	case "response.output_text.delta", "response.refusal.delta":
		return nil, s.addText(e.OutputIndex, e.Delta)
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		// 前者是 OpenAI 的思考摘要；后者是部分兼容服务直接给出的思考原文。
		return nil, s.addReasoning(e.OutputIndex, e.Delta)
	case "response.reasoning_summary_part.added":
		if e.SummaryIndex > 0 && s.thinkIdx[e.OutputIndex] {
			return nil, s.addReasoning(e.OutputIndex, "\n\n")
		}
	case "response.output_item.added":
		if e.Item != nil && e.Item.Type == "function_call" {
			return nil, s.finishItem(e.OutputIndex, *e.Item)
		}
	case "response.function_call_arguments.delta":
		s.call(e.OutputIndex).args.WriteString(e.Delta)
	case "response.function_call_arguments.done":
		c := s.call(e.OutputIndex)
		c.args.Reset()
		c.args.WriteString(e.Arguments)
	case "response.output_item.done":
		if e.Item != nil {
			return nil, s.finishItem(e.OutputIndex, *e.Item)
		}
	case "response.completed", "response.incomplete":
		done := e.Response
		if done == nil {
			done = &responsesDone{}
		}
		if done.Status == "" {
			done.Status = strings.TrimPrefix(e.Type, "response.")
		}
		// 用最终 output 兜底，补上流里漏发的内容。
		for i, it := range done.Output {
			if err := s.finishItem(i, it); err != nil {
				return nil, err
			}
		}
		return done, nil
	case "response.failed":
		code, msg := "", "模型生成失败"
		if e.Response != nil && e.Response.Error != nil {
			code = e.Response.Error.Code
			if e.Response.Error.Message != "" {
				msg = e.Response.Error.Message
			}
		}
		return nil, streamError(httpStatus, code, msg)
	case "error":
		msg := e.Message
		if msg == "" {
			msg = "服务端报告了错误"
		}
		return nil, streamError(httpStatus, e.Code, msg)
	}
	return nil, nil
}

// native 返回本次调用产生的 reasoning 条目，供内核存进 Message.Native。
// 按 output_index 排序，保证与原始顺序一致；没有任何 reasoning 条目时返回 nil。
func (s *respStream) native(model string) *agent.Native {
	if len(s.rawReasoning) == 0 {
		return nil
	}
	idxs := make([]int, 0, len(s.rawReasoning))
	for i := range s.rawReasoning {
		idxs = append(idxs, i)
	}
	slices.Sort(idxs)
	items := make([]json.RawMessage, 0, len(idxs))
	for _, i := range idxs {
		items = append(items, s.rawReasoning[i])
	}
	data, err := json.Marshal(items)
	if err != nil {
		return nil
	}
	return &agent.Native{Protocol: config.ProtocolResponses, Model: model, Data: data}
}

// toolCalls 按 output_index 顺序返回拼好的调用。名字和参数都为空的槽位是残留，丢弃。
func (s *respStream) toolCalls() []agent.ToolCall {
	idxs := make([]int, 0, len(s.calls))
	for i := range s.calls {
		idxs = append(idxs, i)
	}
	slices.Sort(idxs)
	var out []agent.ToolCall
	for _, i := range idxs {
		c := s.calls[i]
		if c.name == "" && c.args.Len() == 0 {
			continue
		}
		id := c.callID
		if id == "" {
			id = newCallID()
		}
		out = append(out, agent.ToolCall{ID: id, Name: c.name, Arguments: c.args.String()})
	}
	return out
}
