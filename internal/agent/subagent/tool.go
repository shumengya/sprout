package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/event"
	"github.com/shumengya/sprout/internal/agent/tool"
)

// ToolName 是委派子代理的工具名。
const ToolName = "agent"

// maxReport 是回给主代理的报告上限，和其他工具的输出上限一致。
const maxReport = 50 << 10

// Runner 是跑一轮子代理所需的最小接口，*agent.Agent 满足它。
type Runner interface {
	Run(ctx context.Context, in agent.Input, sink event.Sink) error
}

// Request 是一次派生请求。
type Request struct {
	Def Definition
	// Tools 是已经按定义筛好的工具表，不含 agent 工具。
	Tools *tool.Registry
	// CallID 是启动它的 agent 工具调用的 ID；不在内核里执行时为空。
	CallID string
}

// Child 是派生出来的子代理。Close 在这一轮结束后调用（关闭记录文件等），可以为 nil。
type Child struct {
	Agent Runner
	Close func()
}

// Spawner 按请求装配一个新的子代理。每次调用都要返回全新的 agent：子代理之间不共享历史。
type Spawner func(Request) (*Child, error)

// Tool 是 agent 工具。
type Tool struct {
	parent *tool.Registry
	defs   map[string]Definition
	order  []string
	spawn  Spawner
	desc   string
	params json.RawMessage
}

// New 创建 agent 工具。parent 是主代理的工具表（agent 工具自己也会注册进去）：
// 每次调用时才从中筛选，所以注册在它之后的工具（外部工具、MCP 工具）同样能交给子代理。
func New(parent *tool.Registry, defs []Definition, spawn Spawner) *Tool {
	t := &Tool{parent: parent, defs: map[string]Definition{}, spawn: spawn}
	for _, d := range defs {
		if _, dup := t.defs[d.Name]; !dup {
			t.order = append(t.order, d.Name)
		}
		t.defs[d.Name] = d
	}
	t.desc = t.describe()
	t.params = t.schema()
	return t
}

func (t *Tool) Name() string                { return ToolName }
func (t *Tool) Description() string         { return t.desc }
func (t *Tool) Parameters() json.RawMessage { return t.params }

// Background 声明 agent 工具可以转入后台：子代理往往要跑很久，主代理没必要干等。
// 是否真的转入后台由模型在每次调用里决定（内核加的 run_in_background 参数）。
func (t *Tool) Background() bool { return true }

// Names 返回可用的子代理类型名，按定义顺序。
func (t *Tool) Names() []string { return append([]string(nil), t.order...) }

func (t *Tool) describe() string {
	var b strings.Builder
	b.WriteString("把一块独立的工作交给子代理去做。子代理有自己的上下文窗口，做完只交回一份文字报告。\n" +
		"适合：\n" +
		"- 要翻很多文件、跑很多命令才能得出结论的调查：中间过程留在子代理里，主对话只拿结论，能省下大量上下文。\n" +
		"- 互相独立的几件事：同一次回复里发出的多个 agent 调用会并行执行。\n" +
		"不适合：一两次工具调用就能做完的事（自己做更快）；需要和用户来回确认的事。\n\n" +
		"使用要点：\n" +
		"- 子代理看不到当前对话，也看不到其他子代理。prompt 要自成一体：背景、目标、范围、已经排除的方向、报告要包含什么，都写进去。\n" +
		"- 派出多个子代理时给它们不重叠的范围；能改文件的子代理并行时尤其如此，否则会互相覆盖。\n" +
		"- 子代理不能再派子代理，也不能向用户提问，也不能在它运行中追加消息；报告是它自己的总结，关键结论需要时自己核实。\n" +
		"- 默认前台调用：等子代理结束、拿到报告再继续，这是首选。只有子代理明显要跑很久、而你在等它的同时确实有别的独立工作要做时，" +
		"才设 run_in_background: true 先派出去，报告之后自动送达。下一步就要用到报告，或者只是想同时派几个子代理" +
		"（同一次回复里的多个 agent 调用本来就并行执行）时，都不要放到后台。\n\n" +
		"子代理类型（agent 参数，默认 " + DefaultAgent + "）：")
	for _, name := range t.order {
		d := t.defs[name]
		tools := "全部工具"
		if d.Tools != nil {
			tools = "工具：" + strings.Join(d.Tools, "、")
			if len(d.Tools) == 0 {
				tools = "没有工具"
			}
		}
		fmt.Fprintf(&b, "\n- %s：%s（%s）", d.Name, d.Description, tools)
	}
	return b.String()
}

func (t *Tool) schema() json.RawMessage {
	names, _ := json.Marshal(t.order)
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "description": {
      "type": "string",
      "description": "3 到 10 个字的任务标题，显示在界面上，如“查找重试逻辑”。"
    },
    "prompt": {
      "type": "string",
      "description": "交给子代理的完整任务说明。子代理看不到当前对话：背景、要做什么、范围、报告写成什么样都要写清楚。"
    },
    "agent": {
      "type": "string",
      "enum": ` + string(names) + `,
      "description": "子代理类型，省略时为 ` + DefaultAgent + `。"
    }
  },
  "required": ["description", "prompt"]
}`)
}

type args struct {
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
	Agent       string `json:"agent"`
}

// Run 派出一个子代理并等它做完。子代理的事件经 tool.Call.Emit 转发给主代理的 sink（包成 event.Subagent），
// 最后一段文字回复作为报告返回。子代理出错不算工具失败：结果里写明原因，退出码为 1，让主代理决定怎么办。
func (t *Tool) Run(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var a args
	if err := json.Unmarshal(raw, &a); err != nil {
		return tool.Result{}, fmt.Errorf("参数不是合法的 JSON：%v", err)
	}
	a.Prompt = strings.TrimSpace(a.Prompt)
	a.Description = strings.Join(strings.Fields(a.Description), " ")
	if a.Agent = strings.TrimSpace(a.Agent); a.Agent == "" {
		a.Agent = DefaultAgent
	}
	if a.Prompt == "" {
		return tool.Result{}, errors.New("prompt 不能为空：子代理看不到当前对话，任务要完整写在 prompt 里")
	}
	def, ok := t.defs[a.Agent]
	if !ok {
		return tool.Result{}, fmt.Errorf("没有名为 %q 的子代理类型，可选 %s", a.Agent, strings.Join(t.order, "、"))
	}
	res := tool.Result{Command: def.Name}
	if a.Description != "" {
		res.Command += ": " + a.Description
	}

	reg := tool.NewRegistry()
	for _, tl := range t.parent.List() {
		if tl.Name() == ToolName || !def.Allows(tl.Name()) {
			continue
		}
		if err := reg.Register(tl); err != nil {
			return res, err
		}
	}
	if len(def.Tools) > 0 && len(reg.Names()) == 0 {
		return res, fmt.Errorf("子代理 %s 声明的工具（%s）当前一个都不可用", def.Name, strings.Join(def.Tools, "、"))
	}

	call, _ := tool.CallFrom(ctx)
	child, err := t.spawn(Request{Def: def, Tools: reg, CallID: call.ID})
	if err != nil {
		return res, fmt.Errorf("无法启动子代理 %s：%w", def.Name, err)
	}
	if child.Close != nil {
		defer child.Close()
	}

	var c collector
	sink := event.SinkFunc(func(e event.Event) {
		c.observe(e)
		switch e.(type) {
		case event.MessageDelta, event.ReasoningDelta:
			return // 增量太多，界面只需要看到子代理在做哪些事
		}
		if call.Emit != nil {
			call.Emit(event.Subagent{CallID: call.ID, Agent: def.Name, Event: e})
		}
	})
	start := time.Now()
	runErr := child.Agent.Run(ctx, agent.Input{Text: a.Prompt}, sink)
	res.Duration = time.Since(start)
	res.Stdout, res.ExitCode = c.report(runErr)
	res.Stdout, res.Truncated = clipReport(res.Stdout)
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	return res, nil
}

// collector 从子代理的事件里记下报告需要的东西。
type collector struct {
	final   string // 最后一次不带工具调用的回复：子代理的结论
	last    string // 最后一段非空文字，子代理没收尾时作为“最后的输出”附上
	reason  string
	errMsg  string
	partial string
}

func (c *collector) observe(e event.Event) {
	switch e := e.(type) {
	case event.MessageCompleted:
		if s := strings.TrimSpace(e.Content); s != "" {
			c.last = s
			if len(e.ToolCalls) == 0 {
				c.final = s
			}
		}
	case event.Interrupted:
		c.partial = strings.TrimSpace(e.Content)
	case event.Error:
		c.errMsg = e.Message
	case event.TurnFinished:
		c.reason = e.Reason
	}
}

// report 生成回给主代理的文本和退出码。
func (c *collector) report(runErr error) (string, int) {
	switch {
	case c.reason == event.ReasonStop && c.final != "":
		return c.final, 0
	case c.reason == event.ReasonLength:
		return "[子代理的输出被长度上限截断，报告可能不完整]\n\n" + c.last, 1
	case c.reason == event.ReasonCancelled:
		return c.withLast("子代理被取消，没有完成任务。"), 1
	case c.reason == event.ReasonStop:
		return "子代理结束了，但没有给出文字报告。", 1
	}
	msg := c.errMsg
	if msg == "" && runErr != nil {
		msg = runErr.Error()
	}
	if msg == "" {
		msg = "未知原因"
	}
	return c.withLast("子代理出错，没有完成任务：" + msg), 1
}

func (c *collector) withLast(head string) string {
	tail := c.last
	if c.partial != "" {
		tail = c.partial
	}
	if tail == "" {
		return head
	}
	return head + "\n\n停下前的最后输出：\n\n" + tail
}

// clipReport 把报告限制在 maxReport 字节以内，按字符边界截断。
func clipReport(s string) (string, bool) {
	if len(s) <= maxReport {
		return s, false
	}
	cut := maxReport
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n\n[报告超过 50KB，后面的部分已截断]", true
}

// SystemSection 是追加在子代理系统提示词末尾的一段：说明它的身份和报告要求，再接上该类型的角色说明。
func SystemSection(d Definition) string {
	var b strings.Builder
	b.WriteString("\n\n## 子代理\n" +
		"你是主代理派出的子代理（类型：" + d.Name + "）。下面这条用户消息是主代理交给你的任务，不是用户本人在和你对话。\n" +
		"- 你看不到主对话，任务说明就是你知道的全部背景；也不能向用户或主代理提问。信息不全时按最合理的理解推进，并在报告里写明你的假设。\n" +
		"- 只做任务说明里要求的事，做完就收尾，不要扩大范围。\n" +
		"- 你的最后一条回复会原样作为工具结果交给主代理，中间过程主代理看不到。把它写成一份完整的报告：" +
		"结论放在最前面，接着是关键依据（文件写成 路径:行号）、做过的改动、没能验证或没做完的部分。不要寒暄，也不要复述任务。")
	if p := strings.TrimSpace(d.Prompt); p != "" {
		b.WriteString("\n\n### 角色\n" + p)
	}
	return b.String()
}
