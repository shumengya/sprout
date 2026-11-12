// Package bash 实现 bash 工具：Windows 上是 Git Bash / MSYS2，其余平台是系统 bash。
package bash

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/shumengya/sprout/internal/agent/platform"
	"github.com/shumengya/sprout/internal/agent/tool"
	"github.com/shumengya/sprout/internal/agent/tool/shell"
)

// Tool 在固定工作目录下用 bash 执行命令。
type Tool struct {
	sh  platform.Shell
	dir string
}

func New(sh platform.Shell, dir string) *Tool {
	return &Tool{sh: sh, dir: dir}
}

func (t *Tool) Name() string { return "bash" }

func (t *Tool) Description() string {
	var b strings.Builder
	fmt.Fprintf(&b, "用 %s 执行一条 bash 命令或脚本，返回 stdout、stderr 和退出码。", t.sh.Label)
	if t.sh.Notes != "" {
		b.WriteString(t.sh.Notes)
	}
	b.WriteString("每次调用都是全新的进程：工作目录固定为启动目录，cd、export 等状态不会保留到下一次调用，需要时在同一条命令里用 && 串联。")
	b.WriteString("不接管 stdin，不要运行需要交互输入的命令；输出超过 50KB 会被截断，默认超时 120 秒。")
	return b.String()
}

func (t *Tool) Parameters() json.RawMessage { return shell.Schema }

// PlatformNotes 是与本机环境相关的说明；tools.jsonc 覆盖描述时它仍会附在后面。
func (t *Tool) PlatformNotes() string {
	s := fmt.Sprintf("当前环境：%s（%s）。", t.sh.Label, t.sh.Path)
	if t.sh.Notes != "" {
		s += t.sh.Notes
	}
	return s
}

func (t *Tool) Run(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	args, timeout, err := shell.ParseArgs(raw)
	if err != nil {
		return tool.Result{}, err
	}
	out, err := shell.Run(ctx, shell.Options{
		Path:    t.sh.Path,
		Args:    []string{"--noprofile", "--norc", "-c", args.Command},
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
