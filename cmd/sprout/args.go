package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
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
}

const usage = `用法:
  sprout [--data-dir DIR] [-c | --resume ID]            启动终端界面
  sprout run [--data-dir DIR] [-c | --resume ID] "提问"   单次运行，答案写 stdout，进度写 stderr
  sprout --version

选项:
  -c, --continue   恢复当前目录最近使用的会话，接着聊
  --resume ID      恢复指定会话：序号（见 TUI 里的 /resume 列表）或 ID 前缀
  --data-dir DIR   数据目录（配置、会话、日志）。优先级: --data-dir > SPROUT_DATA_DIR > <程序所在目录>/data/.sprout
                   用 go run 启动时必须显式指定，例如 --data-dir ./data/.sprout
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
	if rest[0] != "run" {
		return a, fmt.Errorf("%w: 未知命令 %q", errUsage, rest[0])
	}

	// run 子命令：其后的参数也允许出现这些选项。
	rfs := flag.NewFlagSet("sprout run", flag.ContinueOnError)
	register(rfs)
	if err := rfs.Parse(rest[1:]); err != nil {
		return a, fmt.Errorf("%w: %v", errUsage, err)
	}
	finish()
	prompt := strings.TrimSpace(strings.Join(rfs.Args(), " "))
	if prompt == "" {
		return a, fmt.Errorf("%w: run 需要提问内容，例如 sprout run \"看一下当前目录\"", errUsage)
	}
	a.runPrompt = &prompt
	return a, nil
}
