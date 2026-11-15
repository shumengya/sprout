package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/config"
	"github.com/shumengya/sprout/internal/tui"
)

// thinkingFor 把档位换算成内核的思考参数。档位不存在或是 auto 时返回只带名字的零值，即不发送任何参数。
func thinkingFor(sel config.Selection, level string) agent.Thinking {
	l, ok := sel.Model.EffortLevel(level)
	if !ok || l.Empty() {
		return agent.Thinking{Level: level}
	}
	return agent.Thinking{
		Level: level, Effort: l.Effort, Budget: l.Budget, Extra: l.Body,
		Summary: sel.Model.Effort.ShowSummary(),
	}
}

// startEffort 决定启动时的档位：命令行 --effort 优先，其次是上次用过的档位，最后是 models.jsonc 的 default。
//
// saved 只在 restored 为真（上次的模型确实恢复了）时才用：换回 default 模型时沿用为另一个模型选的档位没有意义。
// 记的档位不在当前模型的 levels 里就忽略，退回 default，不报错——模型是用户改配置改掉的，不该挡住启动。
// 命令行指定了模型不支持的档位才是错误：静默忽略会让用户以为在用 max，实际却没发任何参数。
func startEffort(sel config.Selection, flag, saved string, restored bool) (string, error) {
	if flag == "" {
		if saved = strings.ToLower(strings.TrimSpace(saved)); restored && slices.Contains(sel.Model.EffortNames(), saved) {
			return saved, nil
		}
		return sel.Model.DefaultEffort(), nil
	}
	flag = strings.ToLower(strings.TrimSpace(flag))
	names := sel.Model.EffortNames()
	if names == nil {
		return "", fmt.Errorf("模型 %s 没有在 models.jsonc 声明 effort，不能用 --effort", sel.Ref())
	}
	if !slices.Contains(names, flag) {
		return "", fmt.Errorf("模型 %s 没有 %q 档位，可选 %s", sel.Ref(), flag, strings.Join(names, "、"))
	}
	return flag, nil
}

// carryEffort 决定切换模型后的档位：新模型也有同名档位就沿用，否则用它的默认档位。
func carryEffort(sel config.Selection, cur string) string {
	if slices.Contains(sel.Model.EffortNames(), cur) {
		return cur
	}
	return sel.Model.DefaultEffort()
}

// effortLabel 是信息栏显示的档位；模型没有声明 effort 时不显示。
func effortLabel(sel config.Selection, level string) string {
	if sel.Model.Effort == nil {
		return ""
	}
	return level
}

// effortStore 把 app 适配成 tui.Efforts。
type effortStore struct{ a *app }

func (s effortStore) Current() string { return s.a.effort }

func (s effortStore) Choices() []tui.EffortChoice {
	sel := s.a.sel
	names := sel.Model.EffortNames()
	out := make([]tui.EffortChoice, 0, len(names))
	for _, n := range names {
		l, _ := sel.Model.EffortLevel(n)
		out = append(out, tui.EffortChoice{
			Name: n, Detail: config.DescribeEffort(sel.Provider.Protocol, l, sel.Model.Effort.ShowSummary()),
		})
	}
	return out
}

func (s effortStore) Unsupported() string {
	return fmt.Sprintf("模型 %s 没有在 models.jsonc 声明 effort，不能调整思考程度。\n"+
		"在该模型下加上 \"effort\": {\"levels\": [\"low\", \"medium\", \"high\"]} 即可（各家参数不同，写法见 models.example.jsonc），修改后重启 sprout。",
		s.a.sel.Ref())
}

// Set 切换档位。只改 agent 的思考参数，不重建 agent，所以生成中也可以调，下一次请求生效。
func (s effortStore) Set(name string) (string, error) {
	a := s.a
	if a.ag == nil {
		return "", fmt.Errorf("当前没有可用的 agent")
	}
	names := a.sel.Model.EffortNames()
	if names == nil {
		return "", fmt.Errorf("模型 %s 没有声明 effort", a.sel.Ref())
	}
	if !slices.Contains(names, name) {
		return "", fmt.Errorf("没有 %q 档位，可选 %s", name, strings.Join(names, "、"))
	}
	if name == a.effort {
		return "思考程度已经是 " + name + "。", nil
	}
	old := a.effort
	a.effort = name
	a.info.Effort = effortLabel(a.sel, name)
	a.ag.SetThinking(thinkingFor(a.sel, name))
	a.log.Info("effort changed", "model", a.sel.Ref(), "from", old, "to", name)
	a.remember()

	l, _ := a.sel.Model.EffortLevel(name)
	return fmt.Sprintf("思考程度：%s → %s（%s），下一次请求生效。", old, name,
		config.DescribeEffort(a.sel.Provider.Protocol, l, a.sel.Model.Effort.ShowSummary())), nil
}

var _ tui.Efforts = effortStore{}
