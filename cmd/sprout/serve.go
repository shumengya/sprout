package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/event"
	"github.com/shumengya/sprout/internal/cli"
	"github.com/shumengya/sprout/internal/rpc"
)

// rpcBackend 把 app 适配成 rpc.Backend。rpc.Server 保证除 Run 外的方法都在空闲时调用，
// 所以这里可以直接改 app.ag（applyMCP、newSession 都要求没有 Run 在进行）。
type rpcBackend struct {
	a          *app
	mcpApplied bool // 后台连接结束后接入过一次
}

func (b *rpcBackend) Info() rpc.Info {
	a := b.a
	tools := make([]string, 0, len(a.info.Tools))
	for _, t := range a.info.Tools {
		tools = append(tools, t.Name)
	}
	return rpc.Info{
		Version: a.env.version, Model: a.sel.Ref(), Provider: a.sel.ProviderName,
		Cwd: a.cwd, DataDir: a.dataDir, Tools: tools, Vision: a.info.Vision,
		Available: a.ag != nil, Disabled: a.disabled,
		Notices: append(append([]string{}, a.notices...), a.info.Unavailable...),
	}
}

// Prepare 不等 MCP：和 TUI 一样，连好之前的提问先用内置工具，连好后的下一轮再接入。
func (b *rpcBackend) Prepare() []string {
	a := b.a
	apply := false
	if a.mcpReady != nil && !b.mcpApplied {
		select {
		case <-a.mcpReady:
			b.mcpApplied, apply = true, true
		default:
		}
	}
	select {
	case <-a.mcpChanged:
		apply = true
	default:
	}
	if !apply {
		return nil
	}
	_, notices := a.applyMCP()
	return notices
}

// Run 每次取当前的 agent：applyMCP 会替换 app.ag。
func (b *rpcBackend) Run(ctx context.Context, in agent.Input, sink event.Sink) error {
	return b.a.ag.Run(ctx, in, sink)
}

func (b *rpcBackend) NewSession() error { return b.a.newSession() }

var _ rpc.Backend = (*rpcBackend)(nil)

// runServe 实现 sprout serve：stdin / stdout 只走协议消息，日志写文件，启动失败写 stderr。
// 带 -c / --resume 时先恢复会话，失败是错误（和 run 一致：客户端期望的是接着上次的上下文）。
func runServe(ap *app, resume string, stdin io.Reader, stdout, stderr io.Writer) int {
	if resume != "" {
		_, notice, err := ap.resume(resume)
		if err != nil {
			fmt.Fprintln(stderr, "错误: 恢复会话失败:", err)
			return cli.ExitError
		}
		ap.notices = append(ap.notices, notice)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	srv := rpc.NewServer(&rpcBackend{a: ap}, stdout, ap.log)
	if err := srv.Serve(ctx, stdin, 5*time.Second); err != nil {
		return cli.ExitCancelled
	}
	return cli.ExitOK
}
