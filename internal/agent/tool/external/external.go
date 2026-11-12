// Package external 把 tools.jsonc 里定义的命令包装成工具：模型传参数，参数替换进命令行后执行。
//
// 设计来自 pi 的做法：工具只是一份普通定义（名字 + 描述 + JSON Schema 参数 + 执行方式）。
// 这里额外提供的是“执行方式可以是外部命令”，所以 `ls`、`rg` 这类工具不必写 Go 代码。
package external

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/shumengya/sprout/internal/agent/platform"
	"github.com/shumengya/sprout/internal/agent/tool"
	"github.com/shumengya/sprout/internal/agent/tool/powershell"
	"github.com/shumengya/sprout/internal/agent/tool/shell"
)

// DefaultParameters 是没写 parameters 时使用的空对象 schema：模型只能传一个空参数对象。
var DefaultParameters = json.RawMessage(`{"type": "object", "properties": {}}`)

// Spec 是外部工具的配置，与 config.ExecSpec 对应；这里再定义一份是为了让 tool 子包不依赖 config。
type Spec struct {
	Name           string
	Description    string
	Parameters     json.RawMessage
	Shell          string // bash / powershell / direct
	Command        string
	Args           []string
	TimeoutSeconds int
}

// Tool 是一个由配置定义、由命令实现的工具。
type Tool struct {
	spec Spec
	dir  string
	// sh 是执行时用到的 shell 可执行文件；direct 模式不用，可以为 nil。
	sh platform.Shell
}

// New 构造外部工具。sh 只在 bash / powershell 模式下需要。
func New(spec Spec, dir string, sh platform.Shell) *Tool {
	return &Tool{spec: spec, dir: dir, sh: sh}
}

func (t *Tool) Name() string { return t.spec.Name }

func (t *Tool) Description() string { return t.spec.Description }

func (t *Tool) Parameters() json.RawMessage {
	if len(t.spec.Parameters) > 0 {
		return t.spec.Parameters
	}
	return DefaultParameters
}

func (t *Tool) Run(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	timeout := shell.DefaultTimeout
	if t.spec.TimeoutSeconds > 0 {
		timeout = min(time.Duration(t.spec.TimeoutSeconds)*time.Second, shell.MaxTimeout)
	}
	opts, err := t.command(raw)
	if err != nil {
		return tool.Result{}, err
	}
	opts.Dir = t.dir
	opts.Timeout = timeout

	out, err := shell.Run(ctx, opts)
	return tool.Result{
		Command:   t.display(opts),
		Stdout:    out.Stdout,
		Stderr:    out.Stderr,
		ExitCode:  out.ExitCode,
		Truncated: out.Truncated,
		TimedOut:  out.TimedOut,
		Duration:  out.Duration,
	}, err
}

// display 是回给模型和 TUI 的命令回显：shell 模式下就是那行命令，direct 模式拼成 argv。
func (t *Tool) display(o shell.Options) string {
	if t.spec.Shell != "direct" {
		return o.Args[len(o.Args)-1]
	}
	return strings.Join(append([]string{o.Path}, o.Args...), " ")
}

// command 把模型传入的参数替换进命令行，组装出一次进程执行。
func (t *Tool) command(raw json.RawMessage) (shell.Options, error) {
	values, err := parseValues(raw, t.Parameters())
	if err != nil {
		return shell.Options{}, err
	}

	if t.spec.Shell == "direct" {
		argv := substAll(t.spec.Args, values, rawSubstituter)
		path := subst(t.spec.Command, values, rawSubstituter)
		if strings.TrimSpace(path) == "" {
			return shell.Options{}, fmt.Errorf("工具 %s 的 exec.command 替换后为空", t.spec.Name)
		}
		if len(argv) == 0 {
			// 没写 args 时把 command 按空白拆开，允许 "git status {{dir}}" 这种简写。
			fields := strings.Fields(path)
			path, argv = fields[0], fields[1:]
		}
		return shell.Options{Path: path, Args: argv}, nil
	}

	if t.sh.Path == "" {
		return shell.Options{}, fmt.Errorf("工具 %s 需要 %s，但本机没有可用的 %s", t.spec.Name, t.spec.Shell, t.spec.Shell)
	}
	line := subst(t.spec.Command, values, t.quote())
	return t.shellOptions(line), nil
}

// shellOptions 按 shell 类型组装命令行，preamble 与内置的 powershell 工具保持一致。
func (t *Tool) shellOptions(line string) shell.Options {
	if t.spec.Shell == "powershell" {
		return shell.Options{Path: t.sh.Path, Args: []string{"-NoProfile", "-NonInteractive", "-Command", preamble() + line}}
	}
	return shell.Options{Path: t.sh.Path, Args: []string{"--noprofile", "--norc", "-c", line}}
}

// quote 返回按当前 shell 转义的替换函数。
func (t *Tool) quote() substituter {
	if t.spec.Shell == "powershell" {
		return psQuote
	}
	return shQuote
}

// parseValues 把模型传入的参数解成名字 → 值。只接受 JSON 对象。
func parseValues(raw json.RawMessage, schema json.RawMessage) (map[string]any, error) {
	values := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, fmt.Errorf("参数不是有效的 JSON 对象: %w", err)
		}
	}
	// 必填项没给时尽早报错，别让命令带着空值跑出难懂的失败。
	var s struct {
		Required []string `json:"required"`
	}
	if json.Unmarshal(schema, &s) == nil {
		for _, name := range s.Required {
			if v, ok := values[name]; !ok || v == nil {
				return nil, fmt.Errorf("缺少必填参数 %q", name)
			}
		}
	}
	return values, nil
}

// placeholder 匹配 {{name}}；name 用工具名的字符集。
var placeholder = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_-]*)\s*\}\}`)

// substituter 把值渲染成可以直接放进命令行的文本。
type substituter func(v any) string

// subst 替换 s 里的全部占位符。没传的参数替换成空串。
func subst(s string, values map[string]any, sub substituter) string {
	return placeholder.ReplaceAllStringFunc(s, func(m string) string {
		name := placeholder.FindStringSubmatch(m)[1]
		v, ok := values[name]
		if !ok {
			return ""
		}
		return sub(v)
	})
}

func substAll(items []string, values map[string]any, sub substituter) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = subst(s, values, sub)
	}
	return out
}

// scalar 把任意 JSON 值渲染成文本：字符串原样，其他类型按 JSON 序列化。
func scalar(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return fmt.Sprint(x)
		}
		return string(b)
	}
}

// shQuote 把值放进单引号，单引号本身用 '\” 收尾再接续，POSIX shell 下安全。
func shQuote(v any) string {
	// 单引号内不能出现单引号；' 写成 '\''（结束引号 → 转义引号 → 重新开始）。
	return "'" + strings.ReplaceAll(scalar(v), "'", `'\''`) + "'"
}

// psQuote 是 PowerShell 的单引号转义：引号内单引号写成两个单引号。
func psQuote(v any) string {
	return "'" + strings.ReplaceAll(scalar(v), "'", "''") + "'"
}

// rawSubstituter 用于 direct 模式：参数按原样作为独立 argv 项，不需要引号。
func rawSubstituter(v any) string { return scalar(v) }

// preamble 与内置 powershell 工具共用同一份前导，避免两处写法漂移。
func preamble() string { return powershell.Preamble() }

// 编译期确认 Tool 满足接口。
var _ tool.Tool = (*Tool)(nil)
