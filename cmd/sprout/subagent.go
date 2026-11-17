package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/config"
	"github.com/shumengya/sprout/internal/agent/session"
	"github.com/shumengya/sprout/internal/agent/subagent"
	"github.com/shumengya/sprout/internal/agent/tool"
)

// loadSubagents 读取子代理定义：内置类型，加上数据目录 agents/ 与工作目录 .sprout/agents/ 里的 *.md。
// 项目级优先于用户级，用户级优先于内置。写错的定义只跳过并提示，不挡住启动。
func loadSubagents(dataDir, cwd string, cfg *config.File) ([]subagent.Definition, []string) {
	defs, problems := subagent.Load([]subagent.Root{
		{Dir: config.SubagentsDir(dataDir), Source: subagent.SourceUser},
		{Dir: config.ProjectSubagentsDir(cwd), Source: subagent.SourceProject},
	})
	// 模型引用写错要启动时就让用户看到；派出时同样会报错，所以定义照常保留。
	for _, d := range defs {
		if d.Model == "" {
			continue
		}
		if _, err := cfg.Select(d.Model); err != nil {
			problems = append(problems, fmt.Sprintf("子代理 %s（%s）的 model 无效，派出时会失败：%v", d.Name, d.Path, err))
		}
	}
	return defs, problems
}

// spawnSubagent 为一次 agent 工具调用装配子代理。它在主代理的工具 goroutine 里运行。
//
// 读取的 app 字段只在没有 Run 进行时才会被替换（切模型、重建 agent 都在空闲时做），
// sel / key 另有后台读者，按约定加锁读；思考档位一轮中途也能改，所以从主代理本身取，不读 a.effort。
func (a *app) spawnSubagent(req subagent.Request) (*subagent.Child, error) {
	a.mu.Lock()
	sel, key := a.sel, a.key
	a.mu.Unlock()
	var th agent.Thinking
	if a.ag != nil {
		th = a.ag.Thinking()
	}

	d := req.Def
	if d.Model != "" && d.Model != sel.Ref() {
		s, err := a.cfg.Select(d.Model)
		if err != nil {
			return nil, fmt.Errorf("model 无效：%w", err)
		}
		k, err := s.APIKey(a.env.getenv)
		if err != nil {
			return nil, fmt.Errorf("模型 %s：%w（%s）", s.Ref(), err, s.KeyHint())
		}
		sel, key = s, k
		// 主代理的档位是给另一个模型选的，换了模型就用这个模型自己的默认档位。
		th = thinkingFor(sel, sel.Model.DefaultEffort())
	}
	if d.Effort != "" {
		if names := sel.Model.EffortNames(); !slices.Contains(names, d.Effort) {
			return nil, fmt.Errorf("模型 %s 没有 %q 档位（可选：%s）", sel.Ref(), d.Effort, strings.Join(names, "、"))
		}
		th = thinkingFor(sel, d.Effort)
	}
	tools := req.Tools
	if !sel.Model.SupportsTools() {
		tools = tool.NewRegistry() // 与主代理一致：tools=false 的模型不下发任何工具
	}

	system, err := a.systemPrompt(sel, tools, a.env)
	if err != nil {
		return nil, err
	}
	system += subagent.SystemSection(d)

	// 子代理的完整过程记在主会话旁边的 .agents 目录里；主会话还没落盘（不该发生）时就不记。
	var rec *session.Recorder
	if a.rec != nil && req.CallID != "" {
		if p := a.rec.Path(); p != "" {
			rec = session.NewChildRecorder(session.ChildPath(p, req.CallID), session.Info{
				Provider: sel.ProviderName, Model: sel.ModelName, Cwd: a.env.cwd,
				Tools: tools.Names(), Version: a.env.version, Agent: d.Name,
			}, a.log)
		}
	}
	ag, err := a.newAgent(sel, key, tools, system, th, rec, a.env, a.log)
	if err != nil {
		if rec != nil {
			rec.Close()
		}
		return nil, err
	}
	a.log.Info("subagent spawned", "agent", d.Name, "model", sel.Ref(), "tools", len(tools.Names()), "call", req.CallID)
	return &subagent.Child{Agent: ag, Close: func() {
		if rec != nil {
			rec.Close()
		}
	}}, nil
}
