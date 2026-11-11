package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/config"
	"github.com/shumengya/sprout/internal/agent/platform"
	"github.com/shumengya/sprout/internal/agent/session"
	"github.com/shumengya/sprout/internal/agent/tool"
	"github.com/shumengya/sprout/internal/agent/tool/bash"
	"github.com/shumengya/sprout/internal/agent/tool/powershell"
	"github.com/shumengya/sprout/internal/provider/anthropic"
	"github.com/shumengya/sprout/internal/provider/google"
	"github.com/shumengya/sprout/internal/provider/openai"
	"github.com/shumengya/sprout/internal/tui"
)

// env 汇集装配所需的外部输入，测试时可以替换。
type env struct {
	getenv  func(string) string
	cwd     string
	version string
	now     func() time.Time
	detect  func(cfgBash, cfgPS string) platform.Shells
	http    *http.Client       // nil 表示使用 provider 的默认客户端
	retry   *agent.RetryPolicy // nil 表示使用 agent 的默认重试策略；测试用它避免真的退避等待
}

// app 是装配好的运行时。ag 为 nil 表示当前不能对话，原因在 disabled 里。
type app struct {
	dataDir  string
	cwd      string
	cfg      *config.File
	env      env
	log      *slog.Logger
	sel      config.Selection
	reg      *tool.Registry
	ag       *agent.Agent
	rec      *session.Recorder
	info     tui.Info
	disabled string
	notices  []string
}

func (a *app) close() {
	if a.rec != nil {
		a.rec.Close()
	}
}

// buildApp 读取配置并装配 agent。
//
// 配置文件缺失/语法错误/校验失败是致命的，返回 error；
// 缺少 API Key 不是：界面照常启动并显示配置提示，只是不发起请求（app.disabled 非空）。
func buildApp(dataDir string, e env, log *slog.Logger) (*app, error) {
	path := config.ModelsPath(dataDir)
	created, err := config.EnsureTemplate(path)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	sel, err := cfg.Selected()
	if err != nil {
		return nil, err
	}

	a := &app{dataDir: dataDir, cwd: e.cwd, cfg: cfg, env: e, log: log, sel: sel}
	a.info = tui.Info{
		Model: sel.ModelName, Provider: sel.ProviderName + "，" + sel.Provider.Protocol,
		Cwd: e.cwd, DataDir: dataDir, Vision: sel.Model.SupportsVision(),
	}
	reg, err := a.registerTools(cfg, e)
	if err != nil {
		return nil, err
	}

	key, keyErr := sel.APIKey(e.getenv)
	switch {
	case keyErr != nil && created:
		a.disabled = fmt.Sprintf("已生成配置文件 %s\n请填入 API Key 后重启 sprout：%s", path, sel.KeyHint())
	case keyErr != nil:
		a.disabled = fmt.Sprintf("%v\n配置文件：%s；修改后需要重启 sprout。", keyErr, path)
	default:
		if created {
			a.notices = append(a.notices, "已生成配置文件 "+path)
		}
		a.reg = reg
		a.enable(key, reg, e, log)
	}
	return a, nil
}

// registerTools 按检测结果注册 powershell / bash。模型只会看到真实可用的工具。
func (a *app) registerTools(cfg *config.File, e env) (*tool.Registry, error) {
	shells := e.detect(cfg.Shell.BashPath, cfg.Shell.PowerShellPath)
	a.info.Unavailable = nil
	for _, p := range shells.Problems {
		a.info.Unavailable = append(a.info.Unavailable, fmt.Sprintf("%s 不可用：%s", p.Kind, p.Reason))
	}

	reg := tool.NewRegistry()
	a.info.Tools = nil
	if !a.sel.Model.SupportsTools() {
		a.notices = append(a.notices, fmt.Sprintf("模型 %s 在配置中 tools=false，本次不向模型提供任何工具。", a.sel.Ref()))
		return reg, nil
	}
	add := func(sh *platform.Shell, t tool.Tool) error {
		if err := reg.Register(t); err != nil {
			return err
		}
		a.info.Tools = append(a.info.Tools, tui.ToolInfo{Name: t.Name(), Detail: sh.Label + "  " + sh.Path})
		return nil
	}
	if sh := shells.PowerShell; sh != nil {
		if err := add(sh, powershell.New(*sh, e.cwd)); err != nil {
			return nil, err
		}
	}
	if sh := shells.Bash; sh != nil {
		if err := add(sh, bash.New(*sh, e.cwd)); err != nil {
			return nil, err
		}
	}
	return reg, nil
}

// buildAgent 用选定的模型与工具构造 agent。渲染模型切换时也走这里，保证两条路径的装配完全一致。
func (a *app) buildAgent(sel config.Selection, key string, reg *tool.Registry, rec *session.Recorder, e env, log *slog.Logger) (*agent.Agent, error) {
	prov, err := newProvider(sel, key, e, log)
	if err != nil {
		return nil, err
	}
	return agent.New(agent.Config{
		Provider: prov, Model: sel.ModelName, Tools: reg,
		System: agent.BuildSystemPrompt(agent.PromptInfo{
			OS: platform.OSName(), Cwd: e.cwd, Now: e.now(), Tools: reg.Names(),
		}),
		MaxOutputTokens: sel.Model.MaxOutput, ContextWindow: sel.Model.ContextWindow,
		Vision: sel.Model.SupportsVision(),
		Recorder: rec, Retry: e.retry, Logger: log,
	})
}

// enable 构造 provider、会话记录器和 agent。任何一步失败都只是让对话不可用，并说明原因。
func (a *app) enable(key string, reg *tool.Registry, e env, log *slog.Logger) {
	rec := session.NewRecorder(config.SessionsDir(a.dataDir), session.Info{
		Provider: a.sel.ProviderName, Model: a.sel.ModelName, Cwd: e.cwd,
		Tools: reg.Names(), Version: e.version,
	}, log)
	ag, err := a.buildAgent(a.sel, key, reg, rec, e, log)
	if err != nil {
		a.disabled = "无法初始化模型服务: " + err.Error()
		return
	}
	a.ag, a.rec = ag, rec
}

// newProvider 按 protocol 选择适配器。协议名已由 config 校验过，这里的 default 只防手工构造的配置。
func newProvider(sel config.Selection, key string, e env, log *slog.Logger) (agent.Provider, error) {
	p := sel.Provider
	switch p.Protocol {
	case config.ProtocolChat, config.ProtocolResponses:
		return openai.New(openai.Config{
			Protocol: p.Protocol, BaseURL: p.BaseURL, APIKey: key, HTTPClient: e.http, Logger: log,
		})
	case config.ProtocolAnthropic:
		return anthropic.New(anthropic.Config{BaseURL: p.BaseURL, APIKey: key, HTTPClient: e.http, Logger: log})
	case config.ProtocolGoogle:
		return google.New(google.Config{BaseURL: p.BaseURL, APIKey: key, HTTPClient: e.http, Logger: log})
	}
	return nil, fmt.Errorf("不支持的协议 %q", p.Protocol)
}
