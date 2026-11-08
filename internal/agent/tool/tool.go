// Package tool 定义工具接口、执行结果和注册表。
// 具体工具在子包里实现；本包不依赖 agent，由 agent 负责把工具转成 provider 的 ToolSpec。
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Result 是一次工具执行的结果。非零退出码、超时都属于正常结果，不是 error。
type Result struct {
	Command   string
	Stdout    string
	Stderr    string
	ExitCode  int
	Truncated bool
	TimedOut  bool
	Duration  time.Duration
}

// 回给模型的命令回显上限：模型已经在 tool call 里见过完整命令，这里只做对账。
const maxEchoCommand = 500

// ForModel 把结果格式化成回传给模型的文本，必含命令、退出码、stdout、stderr、是否截断。
func (r Result) ForModel() string {
	var b strings.Builder
	cmd := r.Command
	if len(cmd) > maxEchoCommand {
		cmd = cmd[:maxEchoCommand] + "…"
	}
	fmt.Fprintf(&b, "command: %s\n", cmd)
	fmt.Fprintf(&b, "exit_code: %d\n", r.ExitCode)
	fmt.Fprintf(&b, "duration: %s\n", r.Duration.Round(time.Millisecond))
	fmt.Fprintf(&b, "truncated: %t\n", r.Truncated)
	if r.TimedOut {
		b.WriteString("timed_out: true（命令超时被终止）\n")
	}
	if r.Stdout == "" && r.Stderr == "" {
		b.WriteString("\n(无输出)\n")
		return b.String()
	}
	if r.Stdout != "" {
		b.WriteString("\n--- stdout ---\n")
		b.WriteString(r.Stdout)
		if !strings.HasSuffix(r.Stdout, "\n") {
			b.WriteByte('\n')
		}
	}
	if r.Stderr != "" {
		b.WriteString("\n--- stderr ---\n")
		b.WriteString(r.Stderr)
		if !strings.HasSuffix(r.Stderr, "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// Tool 是模型可调用的一个工具。
//
// Run 返回 error 仅表示“没能得到结果”（参数无效、进程起不来、被取消）；
// 命令本身失败应通过 Result.ExitCode 表达。ctx 被取消时应返回 ctx.Err()，
// 并尽量带上已收集的部分输出。
type Tool interface {
	Name() string
	Description() string
	Parameters() json.RawMessage
	Run(ctx context.Context, args json.RawMessage) (Result, error)
}

// Registry 按注册顺序保存工具。注册顺序决定下发给模型的顺序。
type Registry struct {
	order  []Tool
	byName map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{byName: map[string]Tool{}}
}

// Register 注册工具，名字重复时返回错误。
func (r *Registry) Register(t Tool) error {
	name := t.Name()
	if name == "" {
		return fmt.Errorf("工具名称不能为空")
	}
	if _, ok := r.byName[name]; ok {
		return fmt.Errorf("工具 %q 已注册", name)
	}
	r.byName[name] = t
	r.order = append(r.order, t)
	return nil
}

func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.byName[name]
	return t, ok
}

// List 返回按注册顺序排列的工具副本。
func (r *Registry) List() []Tool {
	return append([]Tool(nil), r.order...)
}

// Names 返回按注册顺序排列的工具名。
func (r *Registry) Names() []string {
	names := make([]string, len(r.order))
	for i, t := range r.order {
		names[i] = t.Name()
	}
	return names
}
