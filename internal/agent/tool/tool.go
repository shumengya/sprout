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
	// Images 是随结果一起交给模型的图片（如 MCP 工具返回的截图），接在 ForModel 的文本之后。
	Images []Image
}

// Image 是工具结果里的一张图片。Data 是 base64，不含 data: 前缀。
// 本包不能依赖 agent（agent 依赖本包），所以不复用 agent.Image。
type Image struct {
	MediaType string
	Data      string
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
// Run 可能被并发调用：同一批 tool call 里声明了可并行的会并行执行，
// 实现必须并发安全。没实现 Parallelizable 的工具按可并行处理。
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

// Parallelizable 由需要声明“同一批里的多个调用能不能并行”的工具实现。
//
// 返回 false 表示这个工具的调用要独占执行：等同一批里排在它前面的调用全部结束才开始，
// 执行期间不启动别的调用，结束后才轮到后面的。不实现这个接口等于返回 true。
// 典型场景是改文件的工具：两个 edit 并发改同一个文件会互相覆盖。
type Parallelizable interface {
	Parallel() bool
}

// Backgroundable 由可以转入后台执行的工具实现。
//
// 返回 true 时，内核在这个工具的参数 schema 里加一个布尔参数 BackgroundParam：模型把它设为 true，
// 调用就立即以一段占位说明作为工具结果返回，真正的执行在后台继续，ReAct 循环接着请求模型；
// 执行结束后，结果作为一条用户消息在下一个安全点（一批工具执行完后，或模型给出最终答案时）送进历史。
// 后台任务属于发起它的那一轮：一轮要等它们全部结束才收尾，取消这一轮也会取消它们。
//
// 不实现这个接口等于返回 false。声明不可并行（Parallelizable 返回 false）的工具不能转入后台，
// 后台执行必然和之后的调用重叠，独占执行的承诺无法兑现。
type Backgroundable interface {
	Background() bool
}

// BackgroundParam 是内核加进可后台工具参数里的开关名。执行前内核会把它从参数里去掉，工具自己看不到。
const BackgroundParam = "run_in_background"

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

// Parallel 报告工具的调用能否与同一批里的其他调用并行。没声明的按可并行处理。
func Parallel(t Tool) bool {
	p, ok := t.(Parallelizable)
	return !ok || p.Parallel()
}

// Background 报告工具的调用能否转入后台：必须显式声明可后台，而且可并行。
func Background(t Tool) bool {
	b, ok := t.(Backgroundable)
	return ok && b.Background() && Parallel(t)
}

// Names 返回按注册顺序排列的工具名。
func (r *Registry) Names() []string {
	names := make([]string, len(r.order))
	for i, t := range r.order {
		names[i] = t.Name()
	}
	return names
}

// PlatformNoter 由描述依赖运行环境的工具实现（bash / powershell）：返回当前 shell 的版本与语法限制。
//
// 配置文件覆盖 description 时，这段说明仍会追加在后面。否则用户写死的描述会让模型
// 在 PowerShell 5.1 上用 &&，或者不知道 Windows 上的 bash 是 Git Bash。
type PlatformNoter interface {
	PlatformNotes() string
}

// Decorator 用配置文件里的定义覆盖工具的静态元数据，执行行为保持不变。
//
// 只覆盖非空字段：description / parameters 留空时沿用内置值。
type Decorator struct {
	Tool
	DescriptionText string
	ParamSchema     json.RawMessage
	// ParallelOverride 非 nil 时覆盖工具自己的并行声明（tools.jsonc 的 parallel）。
	ParallelOverride *bool
	// BackgroundOverride 非 nil 时覆盖工具自己的后台声明（tools.jsonc 的 background）。
	BackgroundOverride *bool
}

func (d Decorator) Description() string {
	if d.DescriptionText == "" {
		return d.Tool.Description()
	}
	if n, ok := d.Tool.(PlatformNoter); ok {
		if notes := strings.TrimSpace(n.PlatformNotes()); notes != "" {
			return strings.TrimRight(d.DescriptionText, "\n") + "\n" + notes
		}
	}
	return d.DescriptionText
}

// Parallel 优先用配置里的覆盖值，否则透传被包装工具的声明。
func (d Decorator) Parallel() bool {
	if d.ParallelOverride != nil {
		return *d.ParallelOverride
	}
	return Parallel(d.Tool)
}

// Background 优先用配置里的覆盖值，否则透传被包装工具的声明。可并行的要求由 tool.Background 统一检查。
func (d Decorator) Background() bool {
	if d.BackgroundOverride != nil {
		return *d.BackgroundOverride
	}
	b, ok := d.Tool.(Backgroundable)
	return ok && b.Background()
}

func (d Decorator) Parameters() json.RawMessage {
	if len(d.ParamSchema) > 0 {
		return d.ParamSchema
	}
	return d.Tool.Parameters()
}
