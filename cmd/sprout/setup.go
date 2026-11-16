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
	// interactive 表示当前界面能向用户要输入（TUI）。为 false 时握手不声明 elicitation 与 sampling。
	interactive bool
	// remote 表示输入请求要转给 RPC 客户端（sprout serve），而不是终端界面。
	remote bool
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
	skills  []skills.Skill        // 注册表里已加载的技能；/skills 重新扫描时整体替换
	// skillCfg 是 skills.jsonc 的注册表；skillsDirty 表示改过、等待空闲时重建 agent。
	skillCfg    *config.SkillsFile
	skillsDirty bool
	env         env
	log         *slog.Logger
	sel         config.Selection
	reg         *tool.Registry
	mcp         *mcpSet // 已连接的 MCP 服务端；没有启用任何服务端时为 nil
	// MCP 在后台连接：mcpReady 在连接流程结束时关闭，mcpCancel 在退出时中止它。
	// mu 保护后台 goroutine 与界面线程共用的字段（mcp、info），避免数据竞争。
	mu          sync.Mutex
	mcpVersions *mcp.Versions
	mcpTools    *mcp.ToolCache // 工具列表缓存（mcp-tools.json）
	// mcpAuth 是 OAuth 令牌存储（mcp-auth.json）。各服务端共用一个实例：后台刷新令牌时整份文件读改写，
	// 多个实例各自加锁会互相覆盖。
	mcpAuth    *mcp.AuthStore
	mcpChanged chan struct{} // 服务端通知工具列表变了；TUI 据此在空闲时重建 agent
	input      *mcpInput     // 服务端要用户填表或确认采样时的通道；非交互界面为 nil
	mcpReady   chan struct{}
	mcpCancel  context.CancelFunc
	key        string // 当前模型的 API Key；后台重建 agent 时要用
	ag         *agent.Agent
	rec        *session.Recorder
	info       tui.Info
	effort     string // 当前思考档位，跨 /new、/resume 保留；切模型时尽量沿用
	// statusline 是状态栏布局，来自 statusline.jsonc。
	statusline []tui.StatusItem
	disabled   string
	notices    []string
}

func (a *app) close() {
	if a.input != nil {
		a.input.close() // 之后到达的输入请求直接失败，不再等一个已经退出的界面
	}
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

// remember 把当前的模型和档位记进 settings.jsonc，下次启动直接沿用。
// 只在用户主动切换时调用（run 模式不改）：脚本里临时 --effort 不该覆盖交互时的选择。
// 写失败只记日志，不影响本次使用。
func (a *app) remember() {
	st := config.Settings{Model: a.sel.Ref(), Effort: a.effort}
	if err := config.SaveSettings(config.SettingsPath(a.dataDir), st); err != nil {
		a.log.Warn("保存上次的选择失败", "error", err.Error())
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
	// skills.jsonc 是注册表：扫描到的新技能在这里自动登记，只有注册的才交给模型。
	skillCfg, skillList, skillProblems, err := skillCatalog(dataDir, e.cwd, e.home)
	if err != nil {
		return nil, err
	}
	for _, sk := range skillList {
		log.Info("已加载技能", "name", sk.Name, "source", string(sk.Source), "path", sk.Path)
	}

	// mcp.jsonc 里的服务端是可选扩展：缺文件视为没有，单个服务端连不上只记提示。
	mcpCfg, err := loadMCPConfig(dataDir)
	if err != nil {
		return nil, err
	}

	// statusline.jsonc 决定状态栏显示哪些项、在第几行、什么颜色。
	// 缺失或为空时写模板；项目名写错是配置问题，这里只提示并退回默认布局，不挡住启动。
	statusline, statusProblems, err := loadStatusline(dataDir)
	if err != nil {
		return nil, err
	}

	// models.jsonc 里没写的窗口、输出上限和价格从 models.dev 的本地缓存补，查不到用默认值。
	// 缓存由 `sprout models update` 维护；这里不联网，缺缓存不影响启动。
	// 必须在 Selected 之前：Selection 拷贝的是 Model 值，之后再补全就补不到它身上。
	applyModelsDev(cfg, config.LoadModelsDev(dataDir), log)
	// 旧版把上次的选择记在 state.json，改名后第一次启动把它搬过来，免得丢掉用户的选择。
	if err := config.MigrateStateFile(dataDir); err != nil {
		log.Warn("迁移 state.json 失败", "error", err.Error())
	}
	statePath := config.SettingsPath(dataDir)
	if _, err := config.EnsureSettings(statePath); err != nil {
		return nil, err
	}
	saved, stateErr := config.LoadSettings(statePath)
	// 记录读不出来或记的模型已经删掉，都退回 models.jsonc 的 default：只是个便利，不该挡住启动。
	var stateProblems []string
	sel, err := cfg.Selected()
	if err != nil {
		return nil, err
	}
	restored := false
	switch {
	case stateErr != nil && !config.IsSettingsMissing(stateErr):
		stateProblems = append(stateProblems, fmt.Sprintf("无法读取上次的选择记录，已使用默认模型：%v", stateErr))
	case stateErr == nil && saved.Model != "":
		if s, err := cfg.Select(saved.Model); err == nil {
			sel, restored = s, true
		} else {
			stateProblems = append(stateProblems, fmt.Sprintf("上次使用的模型 %s 已不在 models.jsonc 里，已退回默认模型：%v", saved.Model, err))
		}
	}

	effort, err := startEffort(sel, e.effort, saved.Effort, restored)
	if err != nil {
		return nil, err
	}

	a := &app{
		dataDir: dataDir, cwd: e.cwd, cfg: cfg, tools: toolsCfg, mcpCfg: mcpCfg,
		mcpTools: mcp.LoadTools(config.MCPToolsPath(dataDir)), mcpChanged: make(chan struct{}, 1),
		mcpAuth: mcp.NewAuthStore(config.MCPAuthPath(dataDir)),
		prompt:  prompt, instr: instr, skills: skillList, skillCfg: skillCfg, env: e, log: log, sel: sel, effort: effort,
		statusline: statusline,
	}
	if e.interactive {
		a.input = newMCPInput(e.remote)
	}
	a.notices = append(a.notices, statusProblems...)
	a.notices = append(a.notices, stateProblems...)
	a.notices = append(a.notices, instrProblems...)
	a.notices = append(a.notices, skillProblems...)
	a.info = tui.Info{
		Model: sel.ModelName, Provider: sel.ProviderName + "，" + sel.Provider.Protocol,
		Cwd: e.cwd, DataDir: dataDir, Vision: sel.Model.SupportsVision(), Effort: effortLabel(sel, effort),
		ContextWindow: sel.Model.ContextWindow, Price: tuiPrice(sel.Model.Price),
		Skills: skillInfos(skillList, skillCfg),
	}
	// 模型不支持工具时连 MCP 没有意义：那些工具注册了也发不下去。
	// 连接放到后台：服务端在网络那头，等它们就绪要几秒，界面不该陪着等。
	// 只要配置里有服务端就建集合，哪怕一个都没启用——/mcp 面板要把它们列出来供用户打开。
	if sel.Model.SupportsTools() && len(mcpCfg.Servers) > 0 {
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
		// tools.jsonc 里写了 description / parameters / parallel 时覆盖内置值；外部工具本来就用配置值，包一层也无害。
		if entry, ok := a.tools.Entry(t.Name()); ok && (entry.Description != "" || len(entry.Parameters) > 0 || entry.Parallel != nil) {
			t = tool.Decorator{Tool: t, DescriptionText: entry.Description, ParamSchema: entry.Parameters, ParallelOverride: entry.Parallel}
		}
		if err := reg.Register(t); err != nil {
			return err
		}
		a.info.Tools = append(a.info.Tools, tui.ToolInfo{Name: t.Name(), Detail: detail})
		return nil
	}

	for _, name := range config.KnownTools {
		if !a.tools.Enabled(name) {
			// 区分“配置里根本没写这条”和“写了但关着”：前者常见于升级后才新增的内置工具，
			// 用户按提示补一条即可，不必翻模板找位置。
			if _, ok := a.tools.Entry(name); ok {
				a.info.Unavailable = append(a.info.Unavailable, fmt.Sprintf("%s 未启用（tools.jsonc 中设成了 enable: false）", name))
			} else {
				a.info.Unavailable = append(a.info.Unavailable,
					fmt.Sprintf("%s 未启用（tools.jsonc 里没有这个条目；加上 \"%s\": { \"enable\": true } 即可）", name, name))
			}
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
		case config.ToolEdit:
			if err := add(file.NewEdit(e.cwd), "内置  按原文片段修改文件"); err != nil {
				return nil, err
			}
		case config.ToolDelete:
			if err := add(file.NewDelete(e.cwd), "内置  删除文件或目录（不进回收站）"); err != nil {
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
	// 集合与配置成对取：/mcp 面板会在另一个 goroutine 里换掉它们，分开取会配错版本。
	set, f := a.mcpSnapshot()
	a.info.Unavailable = append(a.info.Unavailable, registerMCPTools(reg, set, f, add, a.log)...)
	return reg, nil
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
	// 只列启用的：/skills 里停用的技能不该再出现在提示词里。
	_, hasRead := reg.Get(config.ToolRead)
	system = skills.AppendCatalog(system, a.activeSkills(), hasRead)
	return agent.New(agent.Config{
		Provider: prov, Model: sel.ModelName, Tools: reg, System: system,
		MaxOutputTokens: sel.Model.MaxOutput, ContextWindow: sel.Model.ContextWindow,
		MaxTokensParam: sel.Model.MaxTokensParam,
		Thinking:       thinkingFor(sel, a.effort),
		Vision:         sel.Model.SupportsVision(),
		Recorder:       rec, Retry: e.retry, Logger: log,
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
