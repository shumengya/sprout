// Package powershell 实现 powershell 工具：优先 pwsh（7+），回退 Windows PowerShell 5.1。
package powershell

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/shumengya/sprout/internal/agent/platform"
	"github.com/shumengya/sprout/internal/agent/tool"
	"github.com/shumengya/sprout/internal/agent/tool/shell"
)

// preamble 在用户命令前执行：
//   - 把控制台输出编码设为无 BOM 的 UTF-8。PowerShell 5.1 重定向后默认走 OEM 代码页，
//     中文会乱码；pwsh 7 同理。try 包裹是因为没有附着控制台时赋值会抛 IOException。
//   - 关闭进度条，否则非交互模式下会往 stderr 写 CLIXML 噪声。
//
// 外部工具（tools.jsonc 里的 exec.shell=powershell）也要用同一份前导，所以这里是导出函数。
func Preamble() string {
	return `$ProgressPreference='SilentlyContinue';` +
		`try{[Console]::OutputEncoding=[System.Text.UTF8Encoding]::new($false);$OutputEncoding=[Console]::OutputEncoding}catch{};`
}

// Tool 在固定工作目录下用 PowerShell 执行命令。
type Tool struct {
	sh  platform.Shell
	dir string
}

func New(sh platform.Shell, dir string) *Tool {
	return &Tool{sh: sh, dir: dir}
}

func (t *Tool) Name() string { return "powershell" }

func (t *Tool) Description() string {
	var b strings.Builder
	fmt.Fprintf(&b, "用 %s 执行一条 PowerShell 命令或脚本，返回 stdout、stderr 和退出码。", t.sh.Label)
	if t.sh.Edition == "7+" {
		b.WriteString("支持现代语法（&&、||、?:、??）。")
	} else {
		b.WriteString("这是 Windows PowerShell 5.1：不支持 &&、||、三元运算符 ?: 和 ??，多条命令用分号 ; 连接，条件执行用 if ($?) { ... }。")
	}
	b.WriteString("对原生程序（git、node 等）不要用 2>&1，它会把 stderr 包成 ErrorRecord；直接读取返回的 stderr 即可。")
	b.WriteString("命令失败时退出码通常是 1，不一定等于原生程序自己的退出码。")
	b.WriteString("每次调用都是全新的进程：工作目录固定为启动目录，Set-Location、变量等状态不会保留到下一次调用。")
	b.WriteString("不接管 stdin，不要运行需要交互输入的命令；输出超过 50KB 会被截断，默认超时 120 秒。")
	return b.String()
}

func (t *Tool) Parameters() json.RawMessage { return shell.Schema }

// PlatformNotes 是与本机 PowerShell 版本相关的说明；tools.jsonc 覆盖描述时它仍会附在后面。
func (t *Tool) PlatformNotes() string {
	s := fmt.Sprintf("当前环境：%s（%s）。", t.sh.Label, t.sh.Path)
	if t.sh.Edition == "7+" {
		return s + "支持现代语法（&&、||、?:、??）。"
	}
	return s + "这是 Windows PowerShell 5.1：不支持 &&、||、三元运算符 ?: 和 ??，多条命令用分号 ; 连接，条件执行用 if ($?) { ... }。"
}

func (t *Tool) Run(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	args, timeout, err := shell.ParseArgs(raw)
	if err != nil {
		return tool.Result{}, err
	}
	out, err := shell.Run(ctx, shell.Options{
		Path:    t.sh.Path,
		Args:    []string{"-NoProfile", "-NonInteractive", "-Command", Preamble() + args.Command},
		Dir:     t.dir,
		Timeout: timeout,
	})
	return tool.Result{
		Command:   args.Command,
		Stdout:    out.Stdout,
		Stderr:    out.Stderr,
		ExitCode:  out.ExitCode,
		Truncated: out.Truncated,
		TimedOut:  out.TimedOut,
		Duration:  out.Duration,
	}, err
}
