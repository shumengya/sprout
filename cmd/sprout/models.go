package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/config"
	"github.com/shumengya/sprout/internal/agent/event"
	"github.com/shumengya/sprout/internal/agent/platform"
	"github.com/shumengya/sprout/internal/tui"
)

// modelStore 把 app 适配成 tui.Models：列模型、切换模型。
//
// 切换只重建 provider 与 agent，对话历史原样带过去；会话记录器不换，
// 切换后的对话仍写进同一个会话文件。TUI 在切换期间禁止发送，所以这里不需要加锁。
type modelStore struct{ a *app }

func (s modelStore) Current() string { return s.a.sel.Ref() }

func (s modelStore) List() []tui.ModelChoice {
	sels := s.a.cfg.All()
	out := make([]tui.ModelChoice, 0, len(sels))
	for _, sel := range sels {
		out = append(out, tui.ModelChoice{
			Ref: sel.Ref(), Provider: sel.ProviderName, Model: sel.ModelName,
			Protocol: sel.Provider.Protocol, Detail: describeModel(sel.Model),
			Current: sel.Ref() == s.a.sel.Ref(),
		})
	}
	return out
}

// describeModel 给选择器一行附加说明：上下文窗口、最大输出、是否支持工具。
func describeModel(m config.Model) string {
	var parts []string
	if m.ContextWindow > 0 {
		parts = append(parts, "上下文 "+shortCount(m.ContextWindow))
	}
	if m.MaxOutput > 0 {
		parts = append(parts, "输出 "+shortCount(m.MaxOutput))
	}
	if !m.SupportsTools() {
		parts = append(parts, "不支持工具")
	}
	if m.SupportsVision() {
		parts = append(parts, "支持图片")
	}
	return strings.Join(parts, " · ")
}

func shortCount(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%gk", float64(n)/1000)
	}
	return fmt.Sprint(n)
}

// Switch 切换到 ref 指向的模型。失败时 app 保持原样，可以继续用旧模型对话。
func (s modelStore) Switch(ref string) (*tui.SwitchResult, error) {
	a := s.a
	if a.ag == nil || a.rec == nil {
		return nil, fmt.Errorf("当前没有可用的 agent")
	}
	sel, err := a.cfg.Select(ref)
	if err != nil {
		return nil, err
	}
	if sel.Ref() == a.sel.Ref() {
		return nil, fmt.Errorf("已经在使用 %s", sel.Ref())
	}
	key, err := sel.APIKey(a.env.getenv)
	if err != nil {
		return nil, err
	}

	// 工具是全局能力，不随模型变；只有 tools=false 的模型不提供工具。
	oldSel, oldReg, oldInfo, oldEffort := a.sel, a.reg, a.info, a.effort
	restore := func() { a.sel, a.reg, a.info, a.effort = oldSel, oldReg, oldInfo, oldEffort }
	a.sel = sel
	a.effort = carryEffort(sel, a.effort)
	a.info.Model, a.info.Provider = sel.ModelName, sel.ProviderName+"，"+sel.Provider.Protocol
	a.info.Vision = sel.Model.SupportsVision()
	a.info.ContextWindow = sel.Model.ContextWindow
	a.info.Price = tuiPrice(sel.Model.Price)
	a.info.Effort = effortLabel(sel, a.effort)
	reg, err := a.registerTools(a.cfg, a.env)
	if err != nil {
		restore()
		return nil, err
	}

	history, summary := a.ag.History(), a.ag.Summary()
	if sel.Ref() != oldSel.Ref() {
		// 换了模型：别家的思维链不该回传给新模型，与恢复旧会话时的处理一致。对话内容保留。
		for i := range history {
			history[i] = history[i].StripReasoning()
		}
	}
	ag, err := a.buildAgent(sel, key, reg, a.rec, a.env, a.log)
	if err != nil {
		restore()
		return nil, err
	}
	ag.Restore(history, summary)
	// key 要跟着换：后台 MCP 连好后 applyMCP 用它重建 agent，留着旧 Key 会打到新模型的服务端上失败。
	a.ag, a.reg, a.key = ag, reg, key
	a.rec.NoteModelChange(sel.ProviderName, sel.ModelName, reg.Names())
	a.remember()

	notice := fmt.Sprintf("已切换模型：%s → %s（保留 %d 条历史消息）", oldSel.Ref(), sel.Ref(), len(history))
	switch {
	case a.info.Effort != "" && a.effort != oldEffort:
		notice += fmt.Sprintf("；思考程度 %s → %s（新模型没有 %s 档位）", oldEffort, a.effort, oldEffort)
	case a.info.Effort != "":
		notice += "；思考程度 " + a.effort
	}
	return &tui.SwitchResult{Info: a.info, Notice: notice}, nil
}

var _ tui.Models = modelStore{}

// runnerFunc 把 app 适配成 tui.Runner，每次都取当前的 agent：
// /model 切换会替换 app.ag，TUI 持有的 Runner 必须跟着变。
type runnerFunc struct{ a *app }

func (r runnerFunc) Run(ctx context.Context, in agent.Input, sink event.Sink) error {
	return r.a.ag.Run(ctx, in, sink)
}

// compactor 把 app 适配成 tui.Compactor，同样每次取当前的 agent：/model 切换会替换 app.ag。
type compactor struct{ a *app }

func (c compactor) Compact(ctx context.Context, focus string, sink event.Sink) error {
	return c.a.ag.Compact(ctx, focus, sink)
}

var _ tui.Compactor = compactor{}

// systemResources 把平台层的整机资源采样适配成 tui.Resources。
func systemResources() func() tui.Resources {
	s := platform.NewSystemSampler()
	return func() tui.Resources {
		u := s.Sample()
		return tui.Resources{Mem: u.MemPercent, CPU: u.CPUPercent, ProcMB: u.ProcMemMB, Load: u.LoadAvg}
	}
}
