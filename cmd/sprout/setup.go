package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/config"
	"github.com/shumengya/sprout/internal/agent/mcp"
	"github.com/shumengya/sprout/internal/agent/platform"
	"github.com/shumengya/sprout/internal/agent/session"
	"github.com/shumengya/sprout/internal/agent/skills"
	"github.com/shumengya/sprout/internal/agent/tool"
	"github.com/shumengya/sprout/internal/agent/tool/bash"
	"github.com/shumengya/sprout/internal/agent/tool/external"
	"github.com/shumengya/sprout/internal/agent/tool/file"
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
	home    string // 用户主目录；为空时不扫描 ~/.agents/skills
	version string
	now     func() time.Time
	detect  func(cfgBash, cfgPS string) platform.Shells
	http    *http.Client       // nil 表示使用 provider 的默认客户端
	retry   *agent.RetryPolicy // nil 表示使用 agent 的默认重试策略；测试用它避免真的退避等待
	effort  string             // 命令行 --effort；空表示用 models.jsonc 的 default
}

// app 是装配好的运行时。ag 为 nil 表示当前不能对话，原因在 disabled 里。
type app struct {
	dataDir string
	cwd     string
	cfg     *config.File
	tools   *config.ToolsFile
	mcpCfg  *config.MCPFile
	prompt  *agent.PromptTemplate // SYSTEMPROMPT.md；切模型、/new 时按同一份模板重新渲染
	instr   []agent.Instruction   // AGENTS.md：启动时读一次，切模型时按同一份重新拼接
	skills  []skills.Skill        // 发现到的技能，启动时扫一次
	env     env
	log     *slog.Logger
	sel     config.Selection
	reg     *tool.Registry
	mcp     *mcpSet // 已连接的 MCP 服务端；没有启用任何服务端时为 nil
	// MCP 在后台连接：mcpReady 在连接流程结束时关闭，mcpCancel 在退出时中止它。
	// mu 保护后台 goroutine 与界面线程共用的字段（mcp、info），避免数据竞争。
	mu          sync.Mutex
	mcpVersions *mcp.Versions
	mcpReady    chan struct{}
	mcpCancel   context.CancelFunc
	key         string // 当前模型的 API Key；后台重建 agent 时要用
	ag          *agent.Agent
	rec         *session.Recorder
	info        tui.Info
	effort      string // 当前思考档位，跨 /new、/resume 保留；切模型时尽量沿用
	disabled    string
	notices     []string
}

func (a *app) close() {
	if a.mcpCancel != nil {
		a.mcpCancel() // 正在连接的服务端随请求取消，不再等它们
	}
	a.waitMCP()
	if a.rec != nil {
		a.rec.Close()
	}
	a.mu.Lock()
	m := a.mcp
	a.mu.Unlock()
	if m != nil {
		m.Close()
	}
}

// buildApp 读取配置并装配 agent。
//
// 配置文件缺失/语法错误/校验失败是致命的，返回 error；
// 缺少 API Key 不是：界面照常启动并显示配置提示，只是不发起请求（app.disabled 非空）。
func buildApp(dataDir string, e env, log *slog.Logger) (*app, error) {
	if log == nil { // 测试和部分调用方不传日志；之后各处直接用 a.log，不再逐个判空
		log = slog.New(slog.DiscardHandler)
	}
	path := config.ModelsPath(dataDir)
	created, err := config.EnsureTemplate(path)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}

	// tools.jsonc 决定哪些工具交给模型；缺失或为空时补上模板（默认全开）。
	toolsPath := config.ToolsPath(dataDir)
	if _, err := config.EnsureToolsTemplate(toolsPath); err != nil {
		return nil, err
	}
	toolsCfg, err := config.LoadTools(toolsPath)
	if err != nil {
		return nil, err
	}

	// SYSTEMPROMPT.md 是系统提示词模板；缺失或为空时补上内置模板。
	prompt, err := loadSystemPrompt(config.SystemPromptPath(dataDir))
	if err != nil {
		return nil, err
	}
	// AGENTS.md 是用户写给 agent 的说明：数据目录一份全局，工作目录各级一份项目说明。
	// 读不了只提示，不影响启动——上级目录里的文件可能根本不归当前用户管。
	instr, instrProblems := loadInstructions(dataDir, e.cwd)
	for _, d := range instr {
		log.Info("已加载 AGENTS.md", "scope", d.Scope, "path", d.Path, "bytes", len(d.Text))
	}

	// 技能是“用到才翻的手册”：启动时只扫元数据，正文由模型自己按需 read。
	skillList, skillProblems := loadSkills(dataDir, e.cwd, e.home)
	for _, sk := range skillList {
		log.Info("已加载技能", "name", sk.Name, "source", string(sk.Source), "path", sk.Path)
	}

	// mcp.jsonc 里的服务端是可选扩展：缺文件视为没有，单个服务端连不上只记提示。
	mcpCfg, err := loadMCPConfig(dataDir)
	if err != nil {
		return nil, err
	}

	// models.jsonc 里没写的窗口、输出上限和价格从 models.dev 的本地缓存补，查不到用默认值。
	// 缓存由 `sprout models update` 维护；这里不联网，缺缓存不影响启动。
	// 必须在 Selected 之前：Selection 拷贝的是 Model 值，之后再补全就补不到它身上。
	applyModelsDev(cfg, config.LoadModelsDev(dataDir), log)
	sel, err := cfg.Selected()
	if err != nil {
		return nil, err
	}

	effort, err := startEffort(sel, e.effort)
	if err != nil {
		return nil, err
	}

	a := &app{
		dataDir: dataDir, cwd: e.cwd, cfg: cfg, tools: toolsCfg, mcpCfg: mcpCfg,
		prompt: prompt, instr: instr, skills: skillList, env: e, log: log, sel: sel, effort: effort,
	}
	a.notices = append(a.notices, instrProblems...)
	a.notices = append(a.notices, skillProblems...)
	a.info = tui.Info{
		Model: sel.ModelName, Provider: sel.ProviderName + "，" + sel.Provider.Protocol,
		Cwd: e.cwd, DataDir: dataDir, Vision: sel.Model.SupportsVision(), Effort: effortLabel(sel, effort),
		ContextWindow: sel.Model.ContextWindow, Price: tuiPrice(sel.Model.Price),
		Skills: skillInfos(skillList),
	}
	// 模型不支持工具时连 MCP 没有意义：那些工具注册了也发不下去。
	// 连接放到后台：服务端在网络那头，等它们就绪要几秒，界面不该陪着等。
	if sel.Model.SupportsTools() && len(mcpCfg.EnabledServers()) > 0 {
		a.startMCP(mcpCfg)
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
		a.key = key
		a.enable(key, reg, e, log)
	}
	return a, nil
}

// registerTools 按 tools.jsonc 的开关注册工具。
//
// 开关只表达“想不想用这个工具”，不表达“本机有没有”：bash / powershell 还必须探测到可执行文件。
// 已启用但本机不可用的工具不进注册表（模型看不到），原因写进 info.Unavailable 供 /tools 展示；
// 被关掉的工具同样记一笔，让用户知道它为什么不见了。
func (a *app) registerTools(cfg *config.File, e env) (*tool.Registry, error) {
	shells := e.detect(cfg.Shell.BashPath, cfg.Shell.PowerShellPath)
	shellProblems := map[string]string{}
	for _, p := range shells.Problems {
		shellProblems[string(p.Kind)] = p.Reason
	}
	// 探测实现没有给出原因时（比如某个平台根本没做这项探测），也别在界面上留个空冒号。
	reason := func(kind string) string {
		if r := shellProblems[kind]; r != "" {
			return r
		}
		return "本机未检测到"
	}

	reg := tool.NewRegistry()
	a.info.Tools = nil
	a.info.Unavailable = nil
	if !a.sel.Model.SupportsTools() {
		a.notices = append(a.notices, fmt.Sprintf("模型 %s 在配置中 tools=false，本次不向模型提供任何工具。", a.sel.Ref()))
		return reg, nil
	}

	add := func(t tool.Tool, detail string) error {
		// tools.jsonc 里写了 description / parameters 时覆盖内置值；外部工具本来就用配置值，包一层也无害。
		if entry, ok := a.tools.Entry(t.Name()); ok && (entry.Description != "" || len(entry.Parameters) > 0) {
			t = tool.Decorator{Tool: t, DescriptionText: entry.Description, ParamSchema: entry.Parameters}
		}
		if err := reg.Register(t); err != nil {
			return err
		}
		a.info.Tools = append(a.info.Tools, tui.ToolInfo{Name: t.Name(), Detail: detail})
		return nil
	}

	for _, name := range config.KnownTools {
		if !a.tools.Enabled(name) {
			a.info.Unavailable = append(a.info.Unavailable, fmt.Sprintf("%s 未启用（tools.jsonc 中未设为 enable: true）", name))
			continue
		}
		switch name {
		case config.ToolBash:
			if sh := shells.Bash; sh != nil {
				if err := add(bash.New(*sh, e.cwd), sh.Label+"  "+sh.Path); err != nil {
					return nil, err
				}
			} else {
				a.info.Unavailable = append(a.info.Unavailable,
					fmt.Sprintf("bash 已在 tools.jsonc 启用，但本机不可用：%s", reason(config.ToolBash)))
			}
		case config.ToolPowerShell:
			if sh := shells.PowerShell; sh != nil {
				if err := add(powershell.New(*sh, e.cwd), sh.Label+"  "+sh.Path); err != nil {
					return nil, err
				}
			} else {
				a.info.Unavailable = append(a.info.Unavailable,
					fmt.Sprintf("powershell 已在 tools.jsonc 启用，但本机不可用：%s", reason(config.ToolPowerShell)))
			}
		case config.ToolRead:
			if err := add(file.NewRead(e.cwd), "内置  按行读取文本文件"); err != nil {
				return nil, err
			}
		case config.ToolWrite:
			if err := add(file.NewWrite(e.cwd), "内置  创建或覆盖文本文件"); err != nil {
				return nil, err
			}
		}
	}

	// 外部工具：tools.jsonc 里非内置名字的条目，按名字排序注册在内置工具之后。
	for _, name := range a.tools.Names() {
		if config.IsBuiltin(name) {
			continue
		}
		entry, _ := a.tools.Entry(name)
		x := entry.Exec
		var sh platform.Shell
		switch x.Shell {
		case config.ExecBash:
			if shells.Bash == nil {
				a.info.Unavailable = append(a.info.Unavailable,
					fmt.Sprintf("%s 需要 bash，但本机不可用：%s", name, reason(config.ToolBash)))
				continue
			}
			sh = *shells.Bash
		case config.ExecPowerShell:
			if shells.PowerShell == nil {
				a.info.Unavailable = append(a.info.Unavailable,
					fmt.Sprintf("%s 需要 powershell，但本机不可用：%s", name, reason(config.ToolPowerShell)))
				continue
			}
			sh = *shells.PowerShell
		}
		t := external.New(external.Spec{
			Name: name, Description: entry.Description, Parameters: entry.Parameters,
			Shell: x.Shell, Command: x.Command, Args: x.Args, TimeoutSeconds: x.TimeoutSeconds,
		}, e.cwd, sh)
		if err := add(t, "外部  "+x.Shell+"  "+x.Command); err != nil {
			return nil, err
		}
	}

	// MCP 工具排在内置与外部工具之后：名字都带 mcp__ 前缀，正常情况下不会撞名，
	// 万一撞了（比如两个服务端都叫同一个名字）先注册的赢，冲突的那条记进 Unavailable。
	a.info.Unavailable = append(a.info.Unavailable,
		registerMCPTools(reg, a.connectedMCP(), a.mcpCfg, add, a.log)...)
	return reg, nil
}

// connectedMCP 返回后台已连好的服务端；仍在连接或没有启用时为 nil。
// 后台 goroutine 会写 a.mcp，所以读取要加锁。
func (a *app) connectedMCP() *mcpSet {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mcp
}

// buildAgent 用选定的模型与工具构造 agent。渲染模型切换时也走这里，保证两条路径的装配完全一致。
func (a *app) buildAgent(sel config.Selection, key string, reg *tool.Registry, rec *session.Recorder, e env, log *slog.Logger) (*agent.Agent, error) {
	prov, err := newProvider(sel, key, e, log)
	if err != nil {
		return nil, err
	}
	system, err := a.prompt.Render(agent.PromptInfo{
		OS: platform.OSName(), Cwd: e.cwd, Now: e.now(), Tools: reg.Names(),
		Vision: sel.Model.SupportsVision(),
	})
	if err != nil {
		return nil, err
	}
	system = agent.AppendInstructions(system, a.instr)
	// 技能目录追加在最后：和 AGENTS.md 一样，做成模板变量会让旧模板静默失效。
	_, hasRead := reg.Get(config.ToolRead)
	system = skills.AppendCatalog(system, a.skills, hasRead)
	return agent.New(agent.Config{
		Provider: prov, Model: sel.ModelName, Tools: reg, System: system,
		MaxOutputTokens: sel.Model.MaxOutput, ContextWindow: sel.Model.ContextWindow,
		Thinking: thinkingFor(sel, a.effort),
		Vision:   sel.Model.SupportsVision(),
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
