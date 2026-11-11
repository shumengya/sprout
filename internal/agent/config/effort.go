package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// EffortAuto 是保留档位：不发送任何思考参数，由服务端按模型默认处理。每个声明了 effort 的模型都有它，
// 不能在 levels 里配置。没写 default 时启动就处在这个档位，请求与没有这个功能时完全一致。
const EffortAuto = "auto"

// EffortOrder 是可配置的档位名，从弱到强。界面按这个顺序排列，不受配置里书写顺序影响。
var EffortOrder = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// Effort 声明一个模型可调的思考程度。没有这个字段的模型不能调节，/effort 会说明原因。
//
// 不同服务商的参数差别很大（OpenAI 是枚举、Gemini 2.5 和老 Claude 是 token 预算、
// DeepSeek 关思考要另发开关），所以每个档位发什么由配置写明，代码里不按模型名猜。
type Effort struct {
	// Default 是启动时的档位；为空表示 auto。
	Default string `json:"default"`
	// Summary 控制是否要求服务端返回可读的思考摘要，缺省为 true。服务端不认这个参数时设成 false。
	Summary *bool `json:"summary"`
	// Levels 是可选档位。数组写法 ["low","high"] 表示强度取值就是档位名（off 不发任何参数）；
	// 对象写法可以给每个档位指定强度字符串、预算数字或完整对象。
	Levels EffortLevels `json:"levels"`
}

// ShowSummary 报告是否要求返回思考摘要。
func (e *Effort) ShowSummary() bool { return e == nil || e.Summary == nil || *e.Summary }

// EffortLevels 是档位名到具体参数的映射。
type EffortLevels map[string]EffortLevel

// EffortLevel 是一个档位要发送的参数。三项都为空表示什么也不发。
type EffortLevel struct {
	// Effort 是强度枚举：OpenAI 的 reasoning_effort / reasoning.effort、Anthropic 的 output_config.effort、
	// Gemini 的 thinkingLevel。
	Effort string `json:"effort"`
	// Budget 是 token 预算：Anthropic 的 budget_tokens（0 表示关闭思考）、Gemini 的 thinkingBudget
	// （0 关闭，-1 动态）。OpenAI 协议没有预算字段。
	Budget *int `json:"budget"`
	// Body 是合并进请求体顶层的额外字段，用于厂商私有开关，例如 DeepSeek 的 {"thinking":{"type":"disabled"}}。
	Body map[string]json.RawMessage `json:"body"`
}

// Empty 报告这个档位是否什么也不发送。
func (l EffortLevel) Empty() bool { return l.Effort == "" && l.Budget == nil && len(l.Body) == 0 }

// UnmarshalJSON 接受数组或对象两种写法。
func (ls *EffortLevels) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	out := EffortLevels{}
	if len(b) > 0 && b[0] == '[' {
		var names []string
		if err := json.Unmarshal(b, &names); err != nil {
			return errors.New("effort.levels 数组里只能写档位名字符串")
		}
		for _, n := range names {
			if _, dup := out[n]; dup {
				return fmt.Errorf("effort.levels 里 %q 写了两次", n)
			}
			if n == "off" {
				out[n] = EffortLevel{} // 数组写法没法表达“怎样关闭”，off 只是不发参数
				continue
			}
			out[n] = EffortLevel{Effort: n}
		}
		*ls = out
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return errors.New("effort.levels 必须是档位名数组或对象")
	}
	for n, v := range raw {
		var l EffortLevel
		if err := l.UnmarshalJSON(v); err != nil {
			return fmt.Errorf("effort.levels.%s：%w", n, err)
		}
		out[n] = l
	}
	*ls = out
	return nil
}

// UnmarshalJSON 接受 null（不发参数）、字符串（强度）、整数（预算）或对象。
func (l *EffortLevel) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	*l = EffortLevel{}
	switch {
	case len(b) == 0 || string(b) == "null":
		return nil
	case b[0] == '"':
		if err := json.Unmarshal(b, &l.Effort); err != nil {
			return err
		}
		if strings.TrimSpace(l.Effort) == "" {
			return errors.New("强度不能是空字符串；什么都不发请写 null")
		}
		return nil
	case b[0] == '-' || (b[0] >= '0' && b[0] <= '9'):
		var n int
		if err := json.Unmarshal(b, &n); err != nil {
			return errors.New("预算必须是整数")
		}
		l.Budget = &n
		return nil
	case b[0] == '{':
		type plain EffortLevel // 去掉方法，避免递归
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields() // 把 budgets、efort 这类笔误当场报出来
		var p plain
		if err := dec.Decode(&p); err != nil {
			return fmt.Errorf("只能包含 effort、budget、body：%v", err)
		}
		*l = EffortLevel(p)
		return nil
	}
	return errors.New("只能写 null、字符串（强度）、整数（预算）或对象")
}

// EffortNames 返回可选档位：auto 在最前，其余按 EffortOrder 排列。没有声明 effort 时返回 nil。
func (m Model) EffortNames() []string {
	if m.Effort == nil {
		return nil
	}
	out := []string{EffortAuto}
	for _, n := range EffortOrder {
		if _, ok := m.Effort.Levels[n]; ok {
			out = append(out, n)
		}
	}
	return out
}

// DefaultEffort 返回启动时的档位。没有声明 effort 时为 auto。
func (m Model) DefaultEffort() string {
	if m.Effort == nil || m.Effort.Default == "" {
		return EffortAuto
	}
	return m.Effort.Default
}

// EffortLevel 返回档位要发送的参数。auto 总是存在且什么也不发；其他档位必须在 levels 里声明。
func (m Model) EffortLevel(name string) (EffortLevel, bool) {
	if name == EffortAuto {
		return EffortLevel{}, true
	}
	if m.Effort == nil {
		return EffortLevel{}, false
	}
	l, ok := m.Effort.Levels[name]
	return l, ok
}

// DescribeEffort 用该协议的真实字段名描述一个档位会发送什么，供 /effort 显示。
func DescribeEffort(protocol string, l EffortLevel, summary bool) string {
	if l.Empty() {
		return "不发送思考参数，由服务端按模型默认处理"
	}
	var parts []string
	switch protocol {
	case ProtocolChat:
		if l.Effort != "" {
			parts = append(parts, "reasoning_effort="+l.Effort)
		}
	case ProtocolResponses:
		if l.Effort != "" {
			s := "reasoning.effort=" + l.Effort
			if summary {
				s += "，summary=auto"
			}
			parts = append(parts, s)
		}
	case ProtocolAnthropic:
		switch {
		case l.Budget != nil && *l.Budget > 0:
			parts = append(parts, fmt.Sprintf("thinking=enabled，budget_tokens=%d", *l.Budget))
		case l.Budget != nil:
			parts = append(parts, "thinking=disabled")
		case l.Effort != "":
			s := "thinking=adaptive"
			if summary {
				s += "（显示摘要）"
			}
			parts = append(parts, s)
		}
		if l.Effort != "" {
			parts = append(parts, "output_config.effort="+l.Effort)
		}
	case ProtocolGoogle:
		if l.Effort != "" {
			parts = append(parts, "thinkingLevel="+l.Effort)
		}
		if l.Budget != nil {
			s := fmt.Sprintf("thinkingBudget=%d", *l.Budget)
			switch *l.Budget {
			case 0:
				s += "（关闭思考）"
			case -1:
				s += "（动态）"
			}
			parts = append(parts, s)
		}
	}
	if len(l.Body) > 0 {
		keys := make([]string, 0, len(l.Body))
		for k := range l.Body {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			parts = append(parts, k+"="+compactJSON(l.Body[k]))
		}
	}
	return strings.Join(parts, "，")
}

func compactJSON(raw json.RawMessage) string {
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return string(raw)
	}
	return b.String()
}

// effortProblems 校验一个模型的 effort 声明。prefix 形如 providers.x.models.y。
func effortProblems(prefix, protocol string, m Model) []string {
	e := m.Effort
	if e == nil {
		return nil
	}
	var out []string
	add := func(format string, args ...any) { out = append(out, prefix+".effort"+fmt.Sprintf(format, args...)) }

	if len(e.Levels) == 0 {
		add(".levels 不能为空，至少声明一个档位，可选 %s", strings.Join(EffortOrder, "、"))
	}
	names := make([]string, 0, len(e.Levels))
	for n := range e.Levels {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		l := e.Levels[n]
		switch {
		case n == EffortAuto:
			add(".levels 不能声明 auto：它是保留档位，表示不发送任何思考参数，始终可选")
			continue
		case !slices.Contains(EffortOrder, n):
			add(".levels 里的 %q 不是已知档位，可选 %s", n, strings.Join(EffortOrder, "、"))
			continue
		}
		if l.Budget != nil {
			b := *l.Budget
			switch protocol {
			case ProtocolChat, ProtocolResponses:
				add(".levels.%s 写了预算 %d，但 %s 协议没有思考预算字段，请改用强度字符串", n, b, protocol)
			case ProtocolAnthropic:
				if b != 0 && b < 1024 {
					add(".levels.%s 的预算 %d 太小：Anthropic 要求 budget_tokens 至少 1024，0 表示关闭思考", n, b)
				}
				if b > 0 && m.MaxOutput > 0 && b >= m.MaxOutput {
					add(".levels.%s 的预算 %d 必须小于 maxOutput（%d）：Anthropic 要求 budget_tokens < max_tokens", n, b, m.MaxOutput)
				}
			case ProtocolGoogle:
				if b < -1 {
					add(".levels.%s 的预算 %d 无效：Gemini 的 thinkingBudget 只能是 -1（动态）、0（关闭）或正数", n, b)
				}
				if l.Effort != "" {
					add(".levels.%s 同时写了强度和预算：Gemini 不允许 thinkingLevel 与 thinkingBudget 同时出现", n)
				}
			}
		}
		for k := range l.Body {
			if strings.TrimSpace(k) == "" {
				add(".levels.%s.body 里有空的字段名", n)
			}
		}
	}
	if d := e.Default; d != "" && d != EffortAuto {
		if _, ok := e.Levels[d]; !ok {
			add(".default %q 不在 levels 里，可选 auto 或已声明的档位", d)
		}
	}
	return out
}
