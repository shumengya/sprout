// Package shell 是 bash / powershell 两个工具共用的进程执行器：
// 超时、输出上限、进程树终止、退出码提取都在这里，工具子包只负责组装命令行。
package shell

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/shumengya/sprout/internal/agent/platform"
)

const (
	DefaultTimeout = 120 * time.Second
	MaxTimeout     = 600 * time.Second
	// MaxOutput 是 stdout 与 stderr 合计保留的字节上限（50KB）。
	MaxOutput = 50 * 1024
	// 进程被杀后等待输出管道关闭的最长时间；孙进程继承了管道时 Wait 否则会一直卡住。
	waitDelay = 2 * time.Second
)

// Options 描述一次进程执行。
type Options struct {
	Path    string   // 可执行文件
	Args    []string // 不含 Path
	Dir     string   // 工作目录
	Timeout time.Duration
	// MaxOutput 为 0 时取 MaxOutput 常量。
	MaxOutput int
}

// Output 是进程执行的原始结果。
type Output struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Truncated bool
	TimedOut  bool
	Duration  time.Duration
}

// Run 执行一次进程并等它结束。
//
//   - 非零退出码、超时：正常返回 Output，err 为 nil。
//   - ctx 被取消：返回已收集的部分 Output 和 ctx.Err()。
//   - 进程起不来：返回 error。
//
// 不接管 stdin（子进程读到 EOF），所以交互式命令会直接失败而不是挂住。
func Run(ctx context.Context, o Options) (Output, error) {
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	limit := o.MaxOutput
	if limit <= 0 {
		limit = MaxOutput
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, o.Path, o.Args...)
	cmd.Dir = o.Dir
	platform.ConfigureCmd(cmd)
	cmd.WaitDelay = waitDelay

	// stdout 占 80%，stderr 占 20%，合计不超过 limit。
	stderrLimit := limit / 5
	stdout, stderr := newCapBuffer(limit-stderrLimit), newCapBuffer(stderrLimit)
	cmd.Stdout, cmd.Stderr = stdout, stderr

	start := time.Now()
	err := cmd.Run()
	out := Output{
		Stdout:    stdout.String(),
		Stderr:    stderr.String(),
		Truncated: stdout.Truncated() || stderr.Truncated(),
		Duration:  time.Since(start),
	}

	switch {
	case err == nil:
		return out, nil
	case ctx.Err() != nil:
		out.ExitCode = -1
		return out, ctx.Err()
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		out.ExitCode, out.TimedOut = -1, true
		return out, nil
	}

	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		out.ExitCode = exitErr.ExitCode()
		return out, nil
	case errors.Is(err, exec.ErrWaitDelay):
		// 进程本身正常退出，只是遗留的后台孙进程还握着输出管道；结果可用。
		return out, nil
	}
	return out, fmt.Errorf("无法启动 %s: %w", o.Path, err)
}
