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
	"github.com/shumengya/sprout/internal/agent/platform"
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
		Cwd: a.cwd, DataDir: a.dataDir, Tools: tools, Vision: a.info.Vision, Effort: a.info.Effort,
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

func (b *rpcBackend) ListSessions(all bool) ([]rpc.SessionInfo, error) {
	list, active, err := b.a.listSessions(all)
	if err != nil {
		return nil, err
	}
	out := make([]rpc.SessionInfo, len(list))
	for i, s := range list {
		out[i] = rpc.SessionInfo{
			ID: s.ID, Path: s.Path, Prompt: s.Prompt, Name: s.Name, Cwd: s.Cwd, Provider: s.Provider, Model: s.Model,
			Updated: s.Updated, Turns: s.Turns, Current: active != "" && platform.SamePath(s.Path, active),
		}
	}
	return out, nil
}

func (b *rpcBackend) ResumeSession(selector string) (rpc.Restored, error) {
	st, notice, err := b.a.resume(selector)
	if err != nil {
		return rpc.Restored{}, err
	}
	return rpc.Restored{ID: st.ID, Path: st.Path, Notice: notice, Turns: st.Turns, Events: st.Events}, nil
}

func (b *rpcBackend) RenameSession(path, name string) error {
	return sessionStore{b.a}.Rename(path, name)
}

func (b *rpcBackend) DeleteSession(path string) error {
	return sessionStore{b.a}.Delete(path)
}

// Compact 同样每次取当前的 agent。
func (b *rpcBackend) Compact(ctx context.Context, focus string, sink event.Sink) error {
	return b.a.ag.Compact(ctx, focus, sink)
}

func (b *rpcBackend) ListModels() []rpc.ModelInfo {
	a := b.a
	sels := a.cfg.All()
	out := make([]rpc.ModelInfo, 0, len(sels))
	for _, sel := range sels {
		out = append(out, rpc.ModelInfo{
			Ref: sel.Ref(), Provider: sel.ProviderName, Model: sel.ModelName, Protocol: sel.Provider.Protocol,
			ContextWindow: sel.Model.ContextWindow, MaxOutput: sel.Model.MaxOutput,
			Tools: sel.Model.SupportsTools(), Vision: sel.Model.SupportsVision(),
			Current: sel.Ref() == a.sel.Ref(),
		})
	}
	return out
}

// SwitchModel 复用 TUI 的 /model，包括记住选择（下次启动沿用）。
// 切到当前模型不算错误：程序调用方常常只想“确保在用某个模型”。
func (b *rpcBackend) SwitchModel(ref string) (string, error) {
	a := b.a
	sel, err := a.cfg.Select(ref)
	if err != nil {
		return "", rpc.NotFoundError{Err: err}
	}
	if sel.Ref() == a.sel.Ref() {
		return "已经在使用 " + sel.Ref(), nil
	}
	res, err := modelStore{a}.Switch(ref)
	if err != nil {
		return "", err
	}
	return res.Notice, nil
}

func (b *rpcBackend) Efforts() rpc.EffortList {
	s := effortStore{b.a}
	l := rpc.EffortList{Current: effortLabel(b.a.sel, b.a.effort)}
	for _, c := range s.Choices() {
		l.Levels = append(l.Levels, rpc.EffortLevel{Name: c.Name, Detail: c.Detail})
	}
	if b.a.sel.Model.Effort == nil {
		l.Unsupported = s.Unsupported()
	}
	return l
}

// SetEffort 复用 TUI 的 /effort。服务端已经排除了不可用的情况，
// 剩下的失败只有模型没声明 effort 和档位不存在，都按“找不到”返回。
func (b *rpcBackend) SetEffort(name string) (string, error) {
	notice, err := effortStore{b.a}.Set(name)
	if err != nil {
		return "", rpc.NotFoundError{Err: err}
	}
	return notice, nil
}

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
	if ap.input != nil {
		ap.input.setForward(rpcForward(srv))
	}
	if err := srv.Serve(ctx, stdin, 5*time.Second); err != nil {
		return cli.ExitCancelled
	}
	return cli.ExitOK
}
