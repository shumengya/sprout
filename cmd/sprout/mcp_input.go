package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/mcp"
	"github.com/shumengya/sprout/internal/tui"
)

// askCap 是向用户要输入的通道容量。服务端的请求是串行到达的，几份就够了；
// 满了就直接拒绝，而不是堵住 MCP 的读循环。
const askCap = 8

// mcpInput 把 MCP 服务端的输入请求（表单、采样确认）转给界面。
// 没有界面时（sprout run）两个函数都为 nil，握手就不声明对应能力。
type mcpInput struct {
	asks  chan *tui.Ask
	mu    sync.Mutex
	alive bool
}

func newMCPInput() *mcpInput {
	return &mcpInput{asks: make(chan *tui.Ask, askCap), alive: true}
}

// close 让还在等待的请求立即失败，并拒绝之后到达的。
func (in *mcpInput) close() {
	in.mu.Lock()
	in.alive = false
	in.mu.Unlock()
}

// ask 把一份请求交给界面并等回答。界面已退出、队列已满或上下文取消时返回错误。
func (in *mcpInput) ask(ctx context.Context, a *tui.Ask) (tui.AskAnswer, error) {
	reply := make(chan tui.AskAnswer, 1)
	a.Reply = reply
	in.mu.Lock()
	if !in.alive {
		in.mu.Unlock()
		return tui.AskAnswer{}, fmt.Errorf("界面已退出")
	}
	select {
	case in.asks <- a:
	default:
		in.mu.Unlock()
		return tui.AskAnswer{}, fmt.Errorf("等待回答的请求太多，已拒绝")
	}
	in.mu.Unlock()

	select {
	case ans := <-reply:
		return ans, nil
	case <-ctx.Done():
		return tui.AskAnswer{}, ctx.Err()
	}
}

// specTweak 给连接配置接上工具列表缓存、变更回调，以及向用户要输入的处理函数。
func (a *app) specTweak() func(*mcp.Spec) {
	return func(s *mcp.Spec) {
		s.Cache = a.mcpTools
		s.OnToolsChanged = a.onMCPToolsChanged
		if a.input == nil {
			return
		}
		s.Elicit = a.elicit
		s.Sample = a.sample
	}
}

// elicit 把服务端的表单转成界面认识的字段，等用户填完再转回。
func (a *app) elicit(ctx context.Context, server string, req mcp.ElicitRequest) (mcp.ElicitResult, error) {
	fields, err := parseElicitSchema(req.Schema)
	if err != nil {
		return mcp.ElicitResult{}, err
	}
	ans, err := a.input.ask(ctx, &tui.Ask{Server: server, Title: "请求输入", Message: req.Message, Fields: fields})
	if err != nil {
		return mcp.ElicitResult{}, err
	}
	return mcp.ElicitResult{Action: ans.Action, Content: ans.Values}, nil
}

// sample 先让用户确认，再把请求交给当前模型生成。
//
// 采样是服务端借用用户的模型与额度，不能静默发生；用户拒绝时返回错误，
// 由协议层转成给服务端的拒绝。
func (a *app) sample(ctx context.Context, server string, req mcp.SampleRequest) (mcp.SampleResult, error) {
	ans, err := a.input.ask(ctx, &tui.Ask{Server: server, Title: "请求使用你的模型", Message: sampleSummary(req)})
	if err != nil {
		return mcp.SampleResult{}, err
	}
	switch ans.Action {
	case tui.AskAccept:
	case tui.AskDecline:
		return mcp.SampleResult{}, fmt.Errorf("用户拒绝了采样请求")
	default:
		return mcp.SampleResult{}, fmt.Errorf("用户取消了采样请求")
	}

	a.mu.Lock()
	sel, key := a.sel, a.key
	a.mu.Unlock()
	prov, err := newProvider(sel, key, a.env, a.log)
	if err != nil {
		return mcp.SampleResult{}, err
	}
	msgs := make([]agent.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		role := agent.RoleUser
		if m.Role == "assistant" {
			role = agent.RoleAssistant
		}
		msgs = append(msgs, agent.Message{Role: role, Content: []agent.Block{{Type: agent.BlockText, Text: m.Text}}})
	}
	res, err := prov.Stream(ctx, agent.Request{
		Model: sel.ModelName, System: req.SystemPrompt, Messages: msgs,
		MaxOutputTokens: req.MaxTokens, MaxTokensParam: sel.Model.MaxTokensParam,
	}, func(agent.Chunk) error { return nil })
	if err != nil {
		return mcp.SampleResult{}, err
	}
	stop := "endTurn"
	if res.StopReason == agent.StopLength {
		stop = "maxTokens"
	}
	return mcp.SampleResult{Text: res.Message.Text(), Model: sel.Ref(), StopReason: stop}, nil
}

// sampleSummary 是给用户看的采样请求摘要：谁要、要生成多少、服务端想用哪个模型。
func sampleSummary(req mcp.SampleRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "服务端要借用当前模型生成一段回复（最多 %d token）。", req.MaxTokens)
	if len(req.ModelHints) > 0 {
		b.WriteString("\n它建议的模型：" + strings.Join(req.ModelHints, "、") + "（实际使用当前模型）")
	}
	if req.SystemPrompt != "" {
		b.WriteString("\n系统提示：" + firstLine(req.SystemPrompt))
	}
	for _, m := range req.Messages {
		b.WriteString("\n" + m.Role + "：" + firstLine(m.Text))
	}
	return b.String()
}

// firstLine 截取首行，避免一条很长的提示把确认框撑满。
func firstLine(s string) string {
	s = strings.SplitN(s, "\n", 2)[0]
	if r := []rune(s); len(r) > 120 {
		return string(r[:120]) + "…"
	}
	return s
}

// parseElicitSchema 把 requestedSchema 转成界面字段。规范要求扁平对象，
// 属性只有 string、number、integer、boolean 与枚举；嵌套或未知类型直接拒绝。
func parseElicitSchema(raw json.RawMessage) ([]tui.AskField, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var schema struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf("requestedSchema 无法解析: %w", err)
	}
	if schema.Type != "" && schema.Type != "object" {
		return nil, fmt.Errorf("requestedSchema 必须是 object，得到 %s", schema.Type)
	}
	required := map[string]bool{}
	for _, n := range schema.Required {
		required[n] = true
	}
	names, err := propertyOrder(raw)
	if err != nil {
		return nil, fmt.Errorf("requestedSchema 无法解析: %w", err)
	}

	fields := make([]tui.AskField, 0, len(names))
	for _, name := range names {
		var p struct {
			Type        string   `json:"type"`
			Title       string   `json:"title"`
			Description string   `json:"description"`
			Enum        []string `json:"enum"`
			EnumNames   []string `json:"enumNames"`
			Default     any      `json:"default"`
		}
		if err := json.Unmarshal(schema.Properties[name], &p); err != nil {
			return nil, fmt.Errorf("字段 %s 无法解析: %w", name, err)
		}
		f := tui.AskField{Name: name, Title: p.Title, Desc: p.Description, Required: required[name]}
		switch {
		case len(p.Enum) > 0:
			f.Kind, f.Choices, f.Labels = "enum", p.Enum, p.EnumNames
			if s, ok := p.Default.(string); ok {
				f.Default = s
			}
		case p.Type == "boolean":
			f.Kind = "boolean"
			if b, ok := p.Default.(bool); ok {
				f.Default = b
			}
		case p.Type == "integer":
			f.Kind = "integer"
			if n, ok := numberDefault(p.Default); ok {
				f.Default = int64(n)
			}
		case p.Type == "number":
			f.Kind = "number"
			if n, ok := numberDefault(p.Default); ok {
				f.Default = n
			}
		case p.Type == "string" || p.Type == "":
			f.Kind = "string"
			if s, ok := p.Default.(string); ok {
				f.Default = s
			}
		default:
			return nil, fmt.Errorf("字段 %s 的类型 %s 不支持", name, p.Type)
		}
		fields = append(fields, f)
	}
	return fields, nil
}

// numberDefault 取出 JSON 数字默认值。encoding/json 把数字解成 float64。
func numberDefault(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case string:
		f, err := strconv.ParseFloat(n, 64)
		return f, err == nil
	}
	return 0, false
}

// propertyOrder 按 JSON 里出现的顺序返回 properties 的键。encoding/json 解成 map 会丢掉顺序，
// 而表单字段的顺序是服务端有意安排的（先填什么后填什么）。
func propertyOrder(raw json.RawMessage) ([]string, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	if _, err := dec.Token(); err != nil { // 顶层 {
		return nil, err
	}
	var names []string
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := keyTok.(string)
		if key != "properties" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, err
			}
			continue
		}
		if _, err := dec.Token(); err != nil { // properties 的 {
			return nil, err
		}
		for dec.More() {
			nameTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			name, _ := nameTok.(string)
			names = append(names, name)
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, err
			}
		}
		if _, err := dec.Token(); err != nil { // properties 的 }
			return nil, err
		}
	}
	return names, nil
}
