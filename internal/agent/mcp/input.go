package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// 服务端向客户端要输入的两种请求：elicitation（向用户收集信息）与 sampling（借用客户端的模型生成）。
// 旧版协议里它们是服务端发来的真正的 JSON-RPC 请求；2026-07-28 里改成工具调用结果里的 input_required
// （MRTR），客户端补全后带着答案重发原请求。两条路最终都落到 Client.input。

// ElicitRequest 是服务端要用户填的一份表单（form 模式）。
type ElicitRequest struct {
	Message string
	// Schema 是 requestedSchema：扁平对象，属性只有 string / number / integer / boolean 与枚举。
	Schema json.RawMessage
}

// ElicitResult 是用户的回答。Action 取 accept、decline、cancel；只有 accept 带 Content。
type ElicitResult struct {
	Action  string         `json:"action"`
	Content map[string]any `json:"content,omitempty"`
}

// Elicitation 的三种动作。
const (
	ElicitAccept  = "accept"
	ElicitDecline = "decline"
	ElicitCancel  = "cancel"
)

// SampleMessage 是采样请求里的一条消息。非文本内容（图片、音频）在解析时换成占位文字。
type SampleMessage struct {
	Role string
	Text string
}

// SampleRequest 是服务端请求客户端用模型生成一段回复。
type SampleRequest struct {
	SystemPrompt string
	Messages     []SampleMessage
	MaxTokens    int
	// ModelHints 是 modelPreferences.hints 里的模型名提示。客户端只有当前这一个模型，仅作展示。
	ModelHints []string
}

// SampleResult 是生成的回复。
type SampleResult struct {
	Text       string
	Model      string
	StopReason string
}

// ElicitFunc 与 SampleFunc 由界面层提供。返回 error 表示用户拒绝或无法处理，
// 会原样转成给服务端的错误。
type (
	ElicitFunc func(ctx context.Context, server string, req ElicitRequest) (ElicitResult, error)
	SampleFunc func(ctx context.Context, server string, req SampleRequest) (SampleResult, error)
)

// clientCapabilities 返回握手时声明的客户端能力：只声明界面真的接了的，
// 规范要求服务端不得使用未声明的能力。
func (s Spec) clientCapabilities() map[string]any {
	caps := map[string]any{}
	if s.Elicit != nil {
		caps["elicitation"] = map[string]any{"form": map[string]any{}}
	}
	if s.Sample != nil {
		caps["sampling"] = map[string]any{}
	}
	return caps
}

// 用户拒绝采样时用的错误码，沿用规范示例里的 -1。
const codeUserRejected = -1

// input 处理一条服务端发来的输入请求，旧版的服务端请求与 MRTR 的 inputRequests 共用。
// 返回值是要放进 result 的对象；失败时是要回给服务端的 JSON-RPC 错误。
func (c *Client) input(ctx context.Context, method string, params json.RawMessage) (any, *rpcError) {
	switch method {
	case "ping":
		return map[string]any{}, nil
	case "roots/list":
		return map[string]any{"roots": []any{}}, nil
	case "elicitation/create":
		if c.spec.Elicit == nil {
			return nil, &rpcError{Code: -32601, Message: "客户端没有启用 elicitation"}
		}
		var p struct {
			Mode    string          `json:"mode"`
			Message string          `json:"message"`
			Schema  json.RawMessage `json:"requestedSchema"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &rpcError{Code: -32602, Message: "elicitation/create 参数无效: " + err.Error()}
		}
		if p.Mode != "" && p.Mode != "form" {
			return nil, &rpcError{Code: -32602, Message: fmt.Sprintf("不支持 %s 模式的 elicitation，只支持 form", p.Mode)}
		}
		res, err := c.spec.Elicit(ctx, c.spec.Name, ElicitRequest{Message: p.Message, Schema: p.Schema})
		if err != nil {
			return nil, &rpcError{Code: -32603, Message: "elicitation 失败: " + err.Error()}
		}
		switch res.Action {
		case ElicitAccept, ElicitDecline, ElicitCancel:
		default:
			return nil, &rpcError{Code: -32603, Message: "elicitation 返回了未知动作 " + res.Action}
		}
		if res.Action != ElicitAccept {
			res.Content = nil
		}
		return res, nil
	case "sampling/createMessage":
		if c.spec.Sample == nil {
			return nil, &rpcError{Code: -32601, Message: "客户端没有启用 sampling"}
		}
		req, err := parseSampleRequest(params)
		if err != nil {
			return nil, &rpcError{Code: -32602, Message: "sampling/createMessage 参数无效: " + err.Error()}
		}
		res, err := c.spec.Sample(ctx, c.spec.Name, req)
		if err != nil {
			return nil, &rpcError{Code: codeUserRejected, Message: err.Error()}
		}
		stop := res.StopReason
		if stop == "" {
			stop = "endTurn"
		}
		model := res.Model
		if model == "" {
			model = "unknown"
		}
		return map[string]any{
			"role":       "assistant",
			"content":    map[string]any{"type": "text", "text": res.Text},
			"model":      model,
			"stopReason": stop,
		}, nil
	}
	return nil, &rpcError{Code: -32601, Message: "不支持的方法: " + method}
}

// parseSampleRequest 解析 sampling/createMessage 的参数。content 可能是单个对象，也可能是数组。
func parseSampleRequest(params json.RawMessage) (SampleRequest, error) {
	var p struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		SystemPrompt     string `json:"systemPrompt"`
		MaxTokens        int    `json:"maxTokens"`
		ModelPreferences struct {
			Hints []struct {
				Name string `json:"name"`
			} `json:"hints"`
		} `json:"modelPreferences"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return SampleRequest{}, err
	}
	if len(p.Messages) == 0 {
		return SampleRequest{}, fmt.Errorf("messages 为空")
	}
	req := SampleRequest{SystemPrompt: p.SystemPrompt, MaxTokens: p.MaxTokens}
	for _, h := range p.ModelPreferences.Hints {
		if h.Name != "" {
			req.ModelHints = append(req.ModelHints, h.Name)
		}
	}
	for _, m := range p.Messages {
		if m.Role != "user" && m.Role != "assistant" {
			return SampleRequest{}, fmt.Errorf("不认识的角色 %q", m.Role)
		}
		blocks := []Content{}
		raw := strings.TrimSpace(string(m.Content))
		switch {
		case raw == "" || raw == "null":
		case raw[0] == '[':
			if err := json.Unmarshal(m.Content, &blocks); err != nil {
				return SampleRequest{}, err
			}
		default:
			var one Content
			if err := json.Unmarshal(m.Content, &one); err != nil {
				return SampleRequest{}, err
			}
			blocks = append(blocks, one)
		}
		var text strings.Builder
		for _, b := range blocks {
			switch b.Type {
			case "text":
				text.WriteString(b.Text)
			case "tool_use", "tool_result":
				// 带工具往返的采样需要工具循环，这里不支持；明说比悄悄丢掉更安全。
				return SampleRequest{}, fmt.Errorf("暂不支持带 %s 的采样消息", b.Type)
			default:
				fmt.Fprintf(&text, "[%s 内容，已省略]", orUnknown(b.Type))
			}
		}
		req.Messages = append(req.Messages, SampleMessage{Role: m.Role, Text: text.String()})
	}
	return req, nil
}

// maxInputRounds 是一次调用最多经历几轮 input_required。服务端一直要输入时不能无限循环。
const maxInputRounds = 8

// inputRequired 是 MRTR 的中间结果。
type inputRequired struct {
	InputRequests map[string]struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	} `json:"inputRequests"`
	RequestState *string `json:"requestState"`
}

// parseInputRequired 判断结果是不是 input_required；不是时 ok 为 false。
func parseInputRequired(raw json.RawMessage) (req inputRequired, ok bool, err error) {
	var probe struct {
		ResultType string `json:"resultType"`
	}
	if json.Unmarshal(raw, &probe) != nil || probe.ResultType != "input_required" {
		return inputRequired{}, false, nil
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return inputRequired{}, true, fmt.Errorf("解析 input_required 结果失败: %w", err)
	}
	if len(req.InputRequests) == 0 && req.RequestState == nil {
		return inputRequired{}, true, fmt.Errorf("input_required 结果既没有 inputRequests 也没有 requestState")
	}
	return req, true, nil
}

// rpc 发一个允许 MRTR 的请求（tools/call、resources/read、prompts/get）：
// 结果是 input_required 时向用户收集输入，带着 inputResponses 用新请求重发，直到拿到最终结果。
//
// 超时按每一次请求单独算：等用户回答的时间不计入服务端的响应时间。
func (c *Client) rpc(ctx context.Context, method, name string, params map[string]any) (json.RawMessage, error) {
	base := params
	for round := 0; ; round++ {
		attempt := make(map[string]any, len(base)+2)
		for k, v := range base {
			attempt[k] = v
		}
		raw, err := c.send(ctx, method, name, attempt)
		if err != nil {
			return nil, err
		}
		req, isInput, err := parseInputRequired(raw)
		if err != nil {
			return nil, err
		}
		if !isInput {
			return raw, nil
		}
		if round >= maxInputRounds {
			return nil, fmt.Errorf("%s 连续 %d 轮要求补充输入，已放弃", method, maxInputRounds)
		}
		responses := make(map[string]any, len(req.InputRequests))
		keys := make([]string, 0, len(req.InputRequests))
		for k := range req.InputRequests {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			ir := req.InputRequests[k]
			res, rerr := c.input(ctx, ir.Method, ir.Params)
			if rerr != nil {
				return nil, fmt.Errorf("%s 需要补充输入（%s），无法完成: %w", method, ir.Method, rerr)
			}
			responses[k] = res
		}
		// 重发带的是原参数加答案；requestState 必须原样回传，且只在服务端给了的时候带。
		next := make(map[string]any, len(params)+2)
		for k, v := range params {
			next[k] = v
		}
		if len(responses) > 0 {
			next["inputResponses"] = responses
		}
		if req.RequestState != nil {
			next["requestState"] = *req.RequestState
		}
		base = next
	}
}

// send 发一次请求，带上协议要求的 _meta 和本次请求自己的超时。
func (c *Client) send(ctx context.Context, method, name string, params map[string]any) (json.RawMessage, error) {
	ctx, done := c.withTimeout(ctx)
	defer done()
	return c.tr.request(ctx, method, name, c.wrap(params))
}

// serverRequest 是传输层收到服务端请求时调用的入口（旧版协议）。
// 等用户回答期间暂停所有进行中请求的计时：用户想一分钟不该让工具调用超时。
func (c *Client) serverRequest(method string, params json.RawMessage) (any, *rpcError) {
	c.beginInput()
	defer c.endInput()
	return c.input(c.life, method, params)
}

// opTimer 是一个可暂停的超时。
type opTimer struct {
	cancel    context.CancelCauseFunc
	remaining time.Duration
	startedAt time.Time
	timer     *time.Timer
}

func (t *opTimer) start() {
	t.startedAt = time.Now()
	t.timer = time.AfterFunc(t.remaining, func() { t.cancel(context.DeadlineExceeded) })
}

func (t *opTimer) pause() {
	if t.timer != nil && t.timer.Stop() {
		t.remaining -= time.Since(t.startedAt)
	}
	t.timer = nil
}

// timers 管理一个客户端上进行中请求的超时。
type timers struct {
	mu     sync.Mutex
	active map[*opTimer]struct{}
	inputs int // 正在等用户输入的数量，>0 时全部暂停
}

func (c *Client) withTimeout(parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	t := &opTimer{cancel: cancel, remaining: c.spec.timeout()}
	c.timers.mu.Lock()
	if c.timers.active == nil {
		c.timers.active = map[*opTimer]struct{}{}
	}
	c.timers.active[t] = struct{}{}
	if c.timers.inputs == 0 {
		t.start()
	}
	c.timers.mu.Unlock()
	return ctx, func() {
		c.timers.mu.Lock()
		delete(c.timers.active, t)
		t.pause()
		c.timers.mu.Unlock()
		cancel(nil)
	}
}

func (c *Client) beginInput() {
	c.timers.mu.Lock()
	defer c.timers.mu.Unlock()
	c.timers.inputs++
	if c.timers.inputs == 1 {
		for t := range c.timers.active {
			t.pause()
		}
	}
}

func (c *Client) endInput() {
	c.timers.mu.Lock()
	defer c.timers.mu.Unlock()
	c.timers.inputs--
	if c.timers.inputs > 0 {
		return
	}
	for t := range c.timers.active {
		if t.remaining < time.Millisecond {
			t.remaining = time.Millisecond
		}
		t.start()
	}
}
