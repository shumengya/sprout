// Command sprout 是 Sprout agent 的入口：无参数启动终端界面，`sprout run "..."` 单次运行。
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/config"
	"github.com/shumengya/sprout/internal/agent/platform"
	"github.com/shumengya/sprout/internal/cli"
	"github.com/shumengya/sprout/internal/media"
	"github.com/shumengya/sprout/internal/tui"
)

// version 由构建时 -ldflags "-X main.version=..." 注入。
var version = "dev"

func main() {
	owns := platform.OwnsConsole() // 双击 exe 启动时为 true：窗口会随进程退出而消失
	restore := platform.InitConsole()
	code := realMain(os.Args[1:], os.Stdout, os.Stderr)
	if code != 0 && owns {
		// 出错时先停住让用户看清错误，否则双击启动的窗口一闪而过。
		fmt.Fprint(os.Stderr, "\n按回车键退出…")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	}
	restore() // 在暂停提示之后执行；os.Exit 不会运行 defer，所以显式调用
	os.Exit(code)
}

// realMain 是不带控制台副作用的主流程，返回进程退出码，便于测试。
func realMain(argv []string, stdout, stderr io.Writer) (code int) {
	log := slog.New(slog.DiscardHandler)
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(stderr, "内部错误: %v\n详细信息已写入日志。\n", r)
			log.Error("panic", "panic", r, "stack", string(debug.Stack()))
			code = cli.ExitError
		}
	}()

	a, err := parseArgs(argv)
	switch {
	case err != nil:
		fmt.Fprintf(stderr, "%v\n\n%s", err, usage)
		return 2
	case a.help:
		fmt.Fprint(stdout, usage)
		return 0
	case a.version:
		fmt.Fprintln(stdout, "sprout", version)
		return 0
	}

	// update 只读写程序本身和网络上的发布信息，不需要数据目录，也不写日志。
	if a.selfUpdate {
		return runUpdate(version, a.updateRepo, nil, a.updateCheckOnly, stdout, stderr)
	}

	// 自己更新过的 Windows 会把旧程序改名成 .old 留在旁边；上次可能因为进程还在而没删掉，
	// 这里顺手清理一次。其他平台没有这个文件，失败也无所谓。
	if exe, err := platform.ResolveExecutable(); err == nil {
		platform.RemoveOldExecutable(exe)
	}

	dataDir, err := config.ResolveDataDir(a.dataDir)
	if err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return cli.ExitError
	}
	logger, closer := openLogger(config.LogsDir(dataDir), time.Now())
	log = logger
	defer closer.Close()

	// models update 只读配置、拉目录，不需要 API Key，也不该装配 agent、建会话文件。
	if a.modelsUpdate {
		return updateModels(dataDir, stdout, stderr)
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "错误: 无法获取当前目录:", err)
		return cli.ExitError
	}
	ap, err := buildApp(dataDir, env{
		getenv: os.Getenv, cwd: cwd, home: homeDir(), version: version, now: time.Now,
		detect: platform.DetectShells, effort: a.effort, interactive: a.runPrompt == nil, remote: a.serve,
	}, log)
	if err != nil {
		log.Error("startup failed", "error", err.Error())
		fmt.Fprintln(stderr, "错误:", err)
		return cli.ExitError
	}
	defer ap.close()
	log.Info("start", "version", version, "data_dir", dataDir, "model", ap.sel.Ref(),
		"protocol", ap.sel.Provider.Protocol, "enabled", ap.ag != nil, "tools", len(ap.info.Tools), "effort", ap.effort)

	if a.runPrompt != nil {
		return runOnce(ap, *a.runPrompt, a.images, a.resume, stdout, stderr)
	}
	if a.serve {
		return runServe(ap, a.resume, os.Stdin, stdout, stderr)
	}
	return runTUI(ap, a.resume, stderr)
}

// homeDir 返回用户主目录；拿不到时返回空串（此时不扫描 ~/.agents/skills）。
func homeDir() string {
	dir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return dir
}

// runOnce 实现 sprout run：答案写 stdout，其余写 stderr。
// 带 -c / --resume 时先恢复会话；找不到可恢复的会话是错误而不是悄悄开新会话——
// 在脚本里，用户期望的是“接着上次的上下文”，静默开新会话会得到完全不同的答案。
func runOnce(ap *app, prompt string, images []string, resume string, stdout, stderr io.Writer) int {
	// MCP 在后台连接：run 模式是单次提问，等到连好再问，才能用上那些工具。
	ap.waitMCP()
	_, notices := ap.applyMCP()
	ap.notices = append(ap.notices, notices...)
	for _, n := range append(append([]string{}, ap.notices...), ap.info.Unavailable...) {
		fmt.Fprintln(stderr, "提示:", n)
	}
	if ap.ag == nil {
		fmt.Fprintln(stderr, "错误:", ap.disabled)
		return cli.ExitError
	}
	in := agent.Input{Text: prompt}
	for _, p := range images {
		img, _, err := media.LoadImage(p)
		if err != nil {
			fmt.Fprintln(stderr, "错误:", err)
			return cli.ExitError
		}
		in.Images = append(in.Images, img)
	}
	// 提问里直接写的图片路径同样当作图片输入。
	var attached []media.Attached
	in, attached, skipped := media.AutoAttach(in)
	for _, a := range attached {
		fmt.Fprintf(stderr, "提示: 已附加图片 %s（%s）\n", a.Name, media.Size(a.Bytes))
	}
	for _, s := range skipped {
		fmt.Fprintln(stderr, "提示: 图片未能附加：", s)
	}
	if len(in.Images) > 0 && !ap.sel.Model.SupportsVision() {
		fmt.Fprintln(stderr, "提示: 当前模型没有标注 vision，图片会被替换成占位文本。")
	}
	if resume != "" {
		_, notice, err := ap.resume(resume)
		if err != nil {
			fmt.Fprintln(stderr, "错误: 恢复会话失败:", err)
			return cli.ExitError
		}
		fmt.Fprintln(stderr, "提示:", notice)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return cli.Run(ctx, ap.ag, in, stdout, stderr)
}

// runTUI 启动终端界面。resume 非空时先恢复会话；失败不致命，只提示并开新会话。
func runTUI(ap *app, resume string, stderr io.Writer) int {
	opts := tui.Options{Info: ap.info, Disabled: ap.disabled, Notices: ap.notices, Installer: commandInstaller{}, Updater: autoUpdater{version: version, repo: releaseRepo}, Resources: systemResources(), Git: gitSampler(ap.info.Cwd), MCPChanged: ap.mcpChanged, Statusline: ap.statusline}
	// 后台连接结束后由 TUI 在空闲时调用 applyMCP，把工具接进 agent 并刷新信息栏。
	if ap.mcpReady != nil {
		opts.MCPReady = ap.mcpReady
		opts.ApplyMCP = ap.applyMCP
	}
	if ap.ag != nil { // 不能直接赋值：nil 指针放进接口后接口本身不为 nil
		opts.Runner = runnerFunc{ap}
		opts.Sessions = sessionStore{ap}
		opts.Models = modelStore{ap}
		opts.Efforts = effortStore{ap}
		opts.Compactor = compactor{ap}
		opts.MCP = mcpStore{ap}
		opts.Skills = skillStore{ap}
		if ap.input != nil {
			opts.Asks = ap.input.asks
		}
		// 面板改了开关后要重建注册表与 agent；没配置 MCP 服务端时 ApplyMCP 只刷展示，无害。
		opts.ApplyMCP = ap.applyMCP
		if resume != "" {
			if st, notice, err := ap.resume(resume); err != nil {
				opts.Notices = append(opts.Notices, "恢复会话失败，已开启新会话："+err.Error())
			} else {
				opts.Replay, opts.ResumeNotice = st.Events, notice
			}
		}
	}
	m := tui.New(opts)
	_, err := tea.NewProgram(m).Run()
	// 取消进行中的一轮并等 agent 收尾，保证最后的 TurnFinished 也写进会话记录。
	m.Shutdown(5 * time.Second)
	if err != nil && !errors.Is(err, tea.ErrInterrupted) {
		fmt.Fprintln(stderr, "错误: 终端界面异常退出:", err)
		return cli.ExitError
	}
	return 0
}
