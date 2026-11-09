package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"slices"
	"sync"

	"github.com/shumengya/sprout/internal/agent/event"
	"github.com/shumengya/sprout/internal/agent/tool"
)

// DefaultMaxSteps 是一轮里最多允许的“模型调用 + 工具执行”次数。
const DefaultMaxSteps = 10

// ErrMaxSteps 表示一轮里模型一直在调用工具，超过了 MaxSteps。
var ErrMaxSteps = errors.New("超过最大工具调用轮数")

// Config 是装配 Agent 所需的依赖，全部通过接口注入。
type Config struct {
	Provider        Provider
	Model           string
	System          string
	Tools           *tool.Registry // 可为 nil，表示没有工具
	MaxOutputTokens int
	MaxSteps        int // <= 0 时取 DefaultMaxSteps
	// MaxParallelTools 是同一批 tool call 同时执行的上限：<= 0 取 DefaultMaxParallelTools，1 表示串行。
	// 工具的 Run 因此必须能被并发调用。
	MaxParallelTools int
	// ContextWindow 是模型的上下文窗口（token）。> 0 时，历史接近窗口会自动压缩；0 表示未知，不主动压缩
	// （服务端报告超长时仍会压缩一次再重试）。
	ContextWindow int
	Recorder      event.Sink   // 可为 nil；与调用方的 sink 收到完全相同的事件，用于会话落盘
	Retry         *RetryPolicy // nil 表示使用 DefaultRetry；&RetryPolicy{} 表示不重试
	Logger        *slog.Logger
}

// Agent 持有一个会话的对话历史。同一时刻只允许一个 Run 在执行。
type Agent struct {
	cfg        Config
	specs      []ToolSpec
	specTokens int
	retry      RetryPolicy
	log        *slog.Logger
	mu         sync.Mutex
	history    []Message
	// summary 是更早对话被压缩后的摘要，每次请求都附在系统提示词后面。
	summary string
	// 校准：服务端上一次报告的真实输入 token，以及当时的历史长度。之后只需估算新增的消息。
	calibTokens int
	calibLen    int
}

// New 校验配置并生成 Agent。
func New(cfg Config) (*Agent, error) {
	if cfg.Provider == nil {
		return nil, errors.New("agent: 必须提供 Provider")
	}
	if cfg.Model == "" {
		return nil, errors.New("agent: 必须指定 Model")
	}
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = DefaultMaxSteps
	}
	a := &Agent{cfg: cfg, log: cfg.Logger, retry: DefaultRetry}
	if cfg.Retry != nil {
		a.retry = *cfg.Retry
	}
	if a.log == nil {
		a.log = slog.New(slog.DiscardHandler)
	}
	if cfg.Tools != nil {
		for _, t := range cfg.Tools.List() {
			a.specs = append(a.specs, ToolSpec{Name: t.Name(), Description: t.Description(), Parameters: t.Parameters()})
			a.specTokens += estimateText(t.Name()) + estimateText(t.Description()) + estimateText(string(t.Parameters()))
		}
	}
	return a, nil
}

// History 返回当前对话历史的副本（不含 system 提示词）。
func (a *Agent) History() []Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.history)
}

// Summary 返回当前的上下文摘要（没有压缩过则为空）。
func (a *Agent) Summary() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.summary
}

// Restore 用保存的历史和摘要替换当前状态，用于恢复会话。会等当前的 Run 结束后再生效。
func (a *Agent) Restore(history []Message, summary string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.history, a.summary = slices.Clone(history), summary
	a.resetCalibration()
}

func (a *Agent) request() Request {
	system := a.cfg.System
	if a.summary != "" {
		system += "\n\n## 之前对话的摘要\n以下是更早对话的摘要（原文已因篇幅被压缩），请把它当作已发生的事实：\n" + a.summary
	}
	return Request{
		Model:           a.cfg.Model,
		System:          system,
		Messages:        slices.Clone(a.history),
		Tools:           a.specs,
		MaxOutputTokens: a.cfg.MaxOutputTokens,
	}
}

// Run 处理用户的一条输入：循环调用模型和工具，直到模型不再请求工具。
// 过程通过 sink（以及 Config.Recorder）以事件形式发出，每轮必定以一个 TurnFinished 结束。
//
// 返回值：nil 表示模型给出了最终答案；ctx 被取消时返回 ctx.Err()；其他失败返回对应的 error。
// 失败的一轮不会在历史里留下悬空的 tool call，之后可以继续 Run。
func (a *Agent) Run(ctx context.Context, prompt string, sink event.Sink) (err error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	t := &turn{a: a, ctx: ctx, out: event.Multi(sink, a.cfg.Recorder), base: len(a.history), reason: event.ReasonStop}
	t.out.Emit(event.TurnStarted{Prompt: prompt})

	defer func() {
		if r := recover(); r != nil {
			// 程序错误：整轮作废，历史退回到本轮开始前，保证后续请求仍然合法。
			a.history = a.history[:t.base]
			a.resetCalibration()
			t.reason = event.ReasonError
			err = fmt.Errorf("内部错误: %v", r)
			a.log.Error("agent panic", "panic", r, "stack", string(debug.Stack()))
			t.out.Emit(event.Error{Message: err.Error()})
		}
		t.out.Emit(event.TurnFinished{Reason: t.reason})
	}()

	a.history = append(a.history, Message{Role: RoleUser, Content: prompt})
	return t.loop()
}
