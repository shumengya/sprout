package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// args 是解析后的命令行。
type args struct {
	dataDir string
	version bool
	help    bool
	// resume 非空表示启动时恢复会话：latest、序号或 ID 前缀。
	resume string
	// runPrompt 非 nil 表示 `sprout run "..."` 单次模式。
	runPrompt *string
	// images 是 run 模式随提问一起发送的图片路径，可重复指定。
	images []string
	// interactive 表示 `sprout run -i`：交互模式，一行一问。
	interactive bool
	// runStdin 表示 run 的参数里写了单独的 "-"：从 stdin 读入内容，接在提问后面。
	runStdin bool
	// effort 是启动时的思考档位，覆盖 models.jsonc 里的 default。
	effort string
	// modelsUpdate 表示 `sprout models update`：拉取 models.dev 目录后退出。
	modelsUpdate bool
	// sessionsClean 表示 `sprout sessions clean`：删除超过 cleanAge 没用过的会话；cleanDry 时只列出。
	// cleanAgeText 是用户写的原文，用于输出。
	sessionsClean bool
	cleanAge      time.Duration
	cleanAgeText  string
	cleanDry      bool
	// selfUpdate 表示 `sprout update`：查询最新发布，默认下载并替换当前程序。
	selfUpdate bool
	// updateCheckOnly 表示 `sprout update --check`：只报告，不下载不替换。
	updateCheckOnly bool
	// updateRepo 是 update 查询的 GitHub 仓库（owner/name），空串用默认仓库。
	updateRepo string
	// serve 表示 `sprout serve`：在 stdin / stdout 上提供 JSON-RPC 服务。
	serve bool
	// mcpAuth 是 `sprout mcp login|logout <名字>` 的动作，mcpServer 是服务端名。
	mcpAuth   string
	mcpServer string
}

// stringList 收集可重复出现的字符串选项（--image a --image b）。
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error {
	if strings.TrimSpace(v) == "" {
		return errors.New("图片路径不能为空")
	}
	*l = append(*l, v)
	return nil
}

const usage = `用法:
  sprout [--data-dir DIR] [-c | --resume ID] [--effort 档位]
                                                   启动终端界面
  sprout run [--data-dir DIR] [-c | --resume ID] [--effort 档位] [--image 文件]... "提问" [-]
                                                   单次运行，答案写 stdout，进度写 stderr；
                                                   不写提问时从 stdin 读，写了 - 时把 stdin 接在提问后面
  sprout run -i [同上选项] ["第一个提问"]          交互模式：不进全屏界面，一行一问，/exit 或 Ctrl+D 退出
  sprout serve [--data-dir DIR] [-c | --resume ID] [--effort 档位]
                                                   在 stdin / stdout 上提供 JSON-RPC 2.0 服务（见 docs/rpc.md）
  sprout models update [--data-dir DIR]            从 models.dev 更新模型参数与价格的本地缓存
  sprout sessions clean --older-than 30d [--dry-run] [--data-dir DIR]
                                                   删除超过这么久没用过的会话（--dry-run 只列出不删）
  sprout mcp login [--data-dir DIR] 服务端名      在浏览器里完成 OAuth 授权，令牌存进数据目录的 mcp-auth.json
  sprout mcp logout [--data-dir DIR] 服务端名     删除保存的 OAuth 令牌
  sprout update [--repo OWNER/NAME] [--check]     更新到 GitHub Releases 上的最新版本（--check 只检查）
  sprout --version

选项:
  -c, --continue   恢复当前目录最近使用的会话，接着聊
  --resume ID      恢复指定会话：序号（见 TUI 里的 /resume 列表）或 ID 前缀
  --image 文件     随提问发送一张图片（png/jpeg/gif/webp），可重复；仅 run 模式支持
  -i, --interactive  run 的交互模式
  --effort 档位    思考程度，如 auto / low / high / max；可选档位由 models.jsonc 里该模型的 effort 声明决定
  --data-dir DIR   数据目录（配置、会话、日志）。优先级: --data-dir > SPROUT_DATA_DIR > <程序所在目录>/data/.sprout
                   用 go run 启动时必须显式指定，例如 --data-dir ./data/.sprout

模型参数:
  contextWindow 与 maxOutput 在 models.jsonc 里手写的值优先；没写的从 models.dev 的本地缓存补，
  缓存里也没有时用默认值（上下文 400000、输出 16000）。缓存由 sprout models update 更新，启动时不联网。
`

var errUsage = errors.New("用法错误")

// parseArgs 解析命令行。--data-dir、-c、--resume 在子命令前后都能写。
func parseArgs(argv []string) (args, error) {
	var a args
	var cont bool
	var resume string
	register := func(fs *flag.FlagSet) {
		fs.SetOutput(io.Discard)
		fs.StringVar(&a.dataDir, "data-dir", a.dataDir, "")
		fs.BoolVar(&cont, "c", cont, "")
		fs.BoolVar(&cont, "continue", cont, "")
		fs.StringVar(&resume, "resume", resume, "")
		fs.StringVar(&a.effort, "effort", a.effort, "")
	}
	// --resume 明确指定的优先于 -c；-c 等价于 --resume latest。
	finish := func() {
		switch {
		case resume != "":
			a.resume = resume
		case cont:
			a.resume = "latest"
		}
	}

	fs := flag.NewFlagSet("sprout", flag.ContinueOnError)
	register(fs)
	fs.BoolVar(&a.version, "version", false, "")
	fs.BoolVar(&a.help, "help", false, "")
	fs.BoolVar(&a.help, "h", false, "")
	if err := fs.Parse(argv); err != nil {
		return a, fmt.Errorf("%w: %v", errUsage, err)
	}
	rest := fs.Args()
	if len(rest) == 0 {
		finish()
		return a, nil
	}
	if rest[0] == "models" {
		return parseModelsArgs(&a, rest[1:])
	}
	if rest[0] == "sessions" {
		return parseSessionsArgs(&a, rest[1:])
	}
	if rest[0] == "update" {
		return parseUpdateArgs(&a, rest[1:])
	}
	if rest[0] == "mcp" {
		return parseMCPArgs(&a, rest[1:])
	}
	if rest[0] == "serve" {
		sfs := flag.NewFlagSet("sprout serve", flag.ContinueOnError)
		register(sfs)
		if err := sfs.Parse(rest[1:]); err != nil {
			return a, fmt.Errorf("%w: %v", errUsage, err)
		}
		if len(sfs.Args()) > 0 {
			return a, fmt.Errorf("%w: sprout serve 不接受额外参数 %q", errUsage, sfs.Args()[0])
		}
		finish()
		a.serve = true
		return a, nil
	}
	if rest[0] != "run" {
		return a, fmt.Errorf("%w: 未知命令 %q", errUsage, rest[0])
	}

	// run 子命令：其后的参数也允许出现这些选项。
	rfs := flag.NewFlagSet("sprout run", flag.ContinueOnError)
	register(rfs)
	rfs.Var((*stringList)(&a.images), "image", "")
	rfs.BoolVar(&a.interactive, "i", false, "")
	rfs.BoolVar(&a.interactive, "interactive", false, "")
	if err := rfs.Parse(rest[1:]); err != nil {
		return a, fmt.Errorf("%w: %v", errUsage, err)
	}
	finish()
	// 单独的 "-" 表示从 stdin 读。没有提问时是否读 stdin 要看它是不是管道，由 runPromptText 在运行时判断。
	var words []string
	for _, w := range rfs.Args() {
		if w == "-" {
			a.runStdin = true
			continue
		}
		words = append(words, w)
	}
	if a.interactive && a.runStdin {
		return a, fmt.Errorf("%w: run -i 用 stdin 逐行提问，不能再用 - 读入", errUsage)
	}
	prompt := strings.TrimSpace(strings.Join(words, " "))
	a.runPrompt = &prompt
	return a, nil
}

// parseModelsArgs 解析 `sprout models update`。目前只有 update 一个子命令，
// 单独放这里，避免以后加子命令时把 parseArgs 撑得更长。
func parseModelsArgs(a *args, argv []string) (args, error) {
	if len(argv) == 0 {
		return *a, fmt.Errorf("%w: sprout models 需要子命令，目前支持 update", errUsage)
	}
	if argv[0] != "update" {
		return *a, fmt.Errorf("%w: sprout models 不支持 %q，目前支持 update", errUsage, argv[0])
	}
	fs := flag.NewFlagSet("sprout models update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&a.dataDir, "data-dir", a.dataDir, "")
	if err := fs.Parse(argv[1:]); err != nil {
		return *a, fmt.Errorf("%w: %v", errUsage, err)
	}
	if len(fs.Args()) > 0 {
		return *a, fmt.Errorf("%w: sprout models update 不接受额外参数 %q", errUsage, fs.Args()[0])
	}
	a.modelsUpdate = true
	return *a, nil
}

// parseSessionsArgs 解析 `sprout sessions clean --older-than 30d [--dry-run]`。
// 删除是不可撤销的，所以 --older-than 必须写，不给默认值。
func parseSessionsArgs(a *args, argv []string) (args, error) {
	if len(argv) == 0 || argv[0] != "clean" {
		return *a, fmt.Errorf("%w: sprout sessions 需要子命令，目前支持 clean", errUsage)
	}
	fs := flag.NewFlagSet("sprout sessions clean", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&a.dataDir, "data-dir", a.dataDir, "")
	fs.StringVar(&a.cleanAgeText, "older-than", "", "")
	fs.BoolVar(&a.cleanDry, "dry-run", false, "")
	if err := fs.Parse(argv[1:]); err != nil {
		return *a, fmt.Errorf("%w: %v", errUsage, err)
	}
	if len(fs.Args()) > 0 {
		return *a, fmt.Errorf("%w: sprout sessions clean 不接受额外参数 %q", errUsage, fs.Args()[0])
	}
	if a.cleanAgeText == "" {
		return *a, fmt.Errorf("%w: sprout sessions clean 需要 --older-than，例如 --older-than 30d", errUsage)
	}
	d, err := parseAge(a.cleanAgeText)
	if err != nil {
		return *a, fmt.Errorf("%w: --older-than %v", errUsage, err)
	}
	a.sessionsClean, a.cleanAge = true, d
	return *a, nil
}

// parseAge 解析清理用的时长：30d 或 30 表示 30 天，其余按 Go 的时长写法（12h、90m）。
func parseAge(s string) (time.Duration, error) {
	if n, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil {
		if n <= 0 {
			return 0, fmt.Errorf("%q 必须大于 0", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%q 不是有效的时长，写成 30d、12h 这样", s)
	}
	return d, nil
}

// parseMCPArgs 解析 `sprout mcp login|logout <服务端名>`。
func parseMCPArgs(a *args, argv []string) (args, error) {
	if len(argv) == 0 || (argv[0] != "login" && argv[0] != "logout") {
		return *a, fmt.Errorf("%w: sprout mcp 需要子命令 login 或 logout", errUsage)
	}
	fs := flag.NewFlagSet("sprout mcp "+argv[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&a.dataDir, "data-dir", a.dataDir, "")
	if err := fs.Parse(argv[1:]); err != nil {
		return *a, fmt.Errorf("%w: %v", errUsage, err)
	}
	// 允许把 --data-dir 写在服务端名后面。
	rest := fs.Args()
	if len(rest) > 1 {
		if err := fs.Parse(rest[1:]); err != nil {
			return *a, fmt.Errorf("%w: %v", errUsage, err)
		}
		if len(fs.Args()) > 0 {
			return *a, fmt.Errorf("%w: sprout mcp %s 只接受一个服务端名", errUsage, argv[0])
		}
	}
	if len(rest) == 0 || strings.TrimSpace(rest[0]) == "" {
		return *a, fmt.Errorf("%w: sprout mcp %s 需要服务端名，例如 sprout mcp %s github", errUsage, argv[0], argv[0])
	}
	a.mcpAuth, a.mcpServer = argv[0], rest[0]
	return *a, nil
}

// parseUpdateArgs 解析 `sprout update [--repo OWNER/NAME] [--check]`。
func parseUpdateArgs(a *args, argv []string) (args, error) {
	fs := flag.NewFlagSet("sprout update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&a.dataDir, "data-dir", a.dataDir, "")
	fs.StringVar(&a.updateRepo, "repo", a.updateRepo, "")
	fs.BoolVar(&a.updateCheckOnly, "check", false, "")
	if err := fs.Parse(argv); err != nil {
		return *a, fmt.Errorf("%w: %v", errUsage, err)
	}
	if len(fs.Args()) > 0 {
		return *a, fmt.Errorf("%w: sprout update 不接受额外参数 %q", errUsage, fs.Args()[0])
	}
	if r := a.updateRepo; r != "" {
		owner, name, ok := strings.Cut(r, "/")
		if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
			return *a, fmt.Errorf("%w: --repo 应写成 OWNER/NAME，得到 %q", errUsage, r)
		}
	}
	a.selfUpdate = true
	return *a, nil
}
