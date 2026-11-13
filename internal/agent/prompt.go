package agent

import (
	"fmt"
	"slices"
	"strings"
	"text/template"
	"time"
)

// PromptInfo 是组装系统提示词所需的运行环境信息。
// 内核不判断 GOOS，操作系统名称由调用方（通过 platform.OSName）传入。
type PromptInfo struct {
	OS  string
	Cwd string
	Now time.Time
	// Tools 是实际可用的工具名，提示词只描述这些工具。
	Tools []string
	// Vision 表示当前模型能直接看到用户附带的图片；为 false 时不写图片相关说明。
	Vision bool
}

// promptData 是模板里 {{.xxx}} 能访问的全部内容。字段名即用户可写的变量名，改名会破坏用户的模板。
type promptData struct {
	OS     string
	Cwd    string
	Date   string
	Now    time.Time
	Tools  []string
	Vision bool
}

// Has 供模板写 {{if .Has "bash"}}：按实际注册的工具决定是否输出某段说明。
func (d promptData) Has(name string) bool { return slices.Contains(d.Tools, name) }

// PromptTemplate 是解析好的系统提示词模板。
type PromptTemplate struct{ t *template.Template }

var promptFuncs = template.FuncMap{"join": strings.Join}

// ParsePromptTemplate 解析模板，并用几组典型环境试渲染一次：
// text/template 只在执行时才发现 {{.Foo}} 这类写错的字段，提前跑一遍才能在启动时报错。
func ParsePromptTemplate(text string) (*PromptTemplate, error) {
	t, err := template.New("SYSTEMPROMPT.md").Funcs(promptFuncs).Option("missingkey=error").Parse(text)
	if err != nil {
		return nil, fmt.Errorf("系统提示词模板语法错误: %w", err)
	}
	p := &PromptTemplate{t: t}
	for _, tools := range [][]string{nil, {"bash", "powershell", "read", "write"}, {"read", "write", "edit", "delete"}} {
		for _, vision := range []bool{false, true} {
			if _, err := p.Render(PromptInfo{OS: "Windows", Cwd: ".", Tools: tools, Vision: vision}); err != nil {
				return nil, err
			}
		}
	}
	return p, nil
}

// Render 用当前环境填充模板。
func (p *PromptTemplate) Render(info PromptInfo) (string, error) {
	var b strings.Builder
	err := p.t.Execute(&b, promptData{
		OS: info.OS, Cwd: info.Cwd, Date: info.Now.Format("2006-01-02"), Now: info.Now,
		Tools: info.Tools, Vision: info.Vision,
	})
	if err != nil {
		return "", fmt.Errorf("渲染系统提示词失败: %w", err)
	}
	return b.String(), nil
}

var defaultPrompt = must(ParsePromptTemplate(DefaultPromptTemplate))

func must(p *PromptTemplate, err error) *PromptTemplate {
	if err != nil {
		panic(err)
	}
	return p
}

// BuildSystemPrompt 用内置模板生成系统提示词。只提到真实可用的工具，模型不会看到不存在的能力。
func BuildSystemPrompt(info PromptInfo) string {
	s, err := defaultPrompt.Render(info)
	if err != nil { // 内置模板在 init 时已试渲染过，走到这里说明模板本身有 bug
		panic(err)
	}
	return s
}
