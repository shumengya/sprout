package config

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// ValidationError 汇总配置里的所有问题，一次告诉用户，避免改一处报一处。
type ValidationError struct {
	Path     string
	Problems []string
}

func (e *ValidationError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "配置文件 %s 无效：", e.Path)
	for _, p := range e.Problems {
		b.WriteString("\n  - ")
		b.WriteString(p)
	}
	return b.String()
}

// problems 返回所有校验问题，顺序稳定。
func (f *File) problems() []string {
	var out []string
	add := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }

	if len(f.Providers) == 0 {
		add("providers 不能为空，至少定义一个 provider")
	}

	names := make([]string, 0, len(f.Providers))
	for name := range f.Providers {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		p := f.Providers[name]
		prefix := "providers." + name
		switch p.Protocol {
		case ProtocolChat, ProtocolResponses:
		case "":
			add("%s.protocol 不能为空，可选 %s、%s", prefix, ProtocolChat, ProtocolResponses)
		default:
			add("%s.protocol %q 不受支持，只允许 %s、%s", prefix, p.Protocol, ProtocolChat, ProtocolResponses)
		}
		if msg := checkBaseURL(p.BaseURL); msg != "" {
			add("%s.baseUrl %s", prefix, msg)
		}
		modelNames := make([]string, 0, len(p.Models))
		for mn := range p.Models {
			modelNames = append(modelNames, mn)
		}
		slices.Sort(modelNames)
		for _, mn := range modelNames {
			m := p.Models[mn]
			if m.ContextWindow < 0 {
				add("%s.models.%s.contextWindow 不能为负数", prefix, mn)
			}
			if m.MaxOutput < 0 {
				add("%s.models.%s.maxOutput 不能为负数", prefix, mn)
			}
		}
	}

	if f.Default == "" {
		add("default 不能为空，格式为 provider/model")
	} else if pn, mn, ok := splitRef(f.Default); !ok {
		add("default %q 必须是 provider/model 形式", f.Default)
	} else if p, ok := f.Providers[pn]; !ok {
		add("default 引用的 provider %q 不存在", pn)
	} else if _, ok := p.Models[mn]; !ok {
		add("default 引用的模型 %q 不在 provider %q 下", mn, pn)
	}
	return out
}

// checkBaseURL 要求 http/https 且有主机名，返回空串表示通过。
func checkBaseURL(raw string) string {
	if raw == "" {
		return "不能为空"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Sprintf("%q 不是有效的 URL", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Sprintf("%q 必须以 http:// 或 https:// 开头", raw)
	}
	if u.Host == "" {
		return fmt.Sprintf("%q 缺少主机名", raw)
	}
	return ""
}
