package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func loadModel(t *testing.T, protocol, model string) (Model, error) {
	t.Helper()
	f, err := Load(write(t, `{
  "default": "p/m",
  "providers": {"p": {"protocol": "`+protocol+`", "baseUrl": "https://x.test/v1", "models": {"m": `+model+`}}}
}`))
	if err != nil {
		return Model{}, err
	}
	return f.Providers["p"].Models["m"], nil
}

// 数组写法：档位名就是强度，off 不发参数；可选档位按强弱排序，auto 永远在最前。
func TestEffortArrayForm(t *testing.T) {
	m, err := loadModel(t, ProtocolChat, `{"effort": {"levels": ["high", "off", "low"]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(m.EffortNames(), ","); got != "auto,off,low,high" {
		t.Errorf("EffortNames = %s", got)
	}
	if l, _ := m.EffortLevel("high"); l.Effort != "high" || l.Budget != nil {
		t.Errorf("high = %+v", l)
	}
	if l, ok := m.EffortLevel("off"); !ok || !l.Empty() {
		t.Errorf("off = %+v，ok=%v", l, ok)
	}
	if m.DefaultEffort() != EffortAuto || !m.Effort.ShowSummary() {
		t.Errorf("默认档位 %q，摘要 %v", m.DefaultEffort(), m.Effort.ShowSummary())
	}
	if _, ok := m.EffortLevel("max"); ok {
		t.Error("没声明的档位不该可用")
	}
}

// 对象写法：字符串是强度，整数是预算，null 是不发，对象可以带私有字段。
func TestEffortObjectForm(t *testing.T) {
	m, err := loadModel(t, ProtocolAnthropic, `{"maxOutput": 64000, "effort": {
    "default": "high", "summary": false,
    "levels": {
      "off": 0,
      "low": 2048,
      "medium": null,
      "high": {"effort": "high", "body": {"x": {"y": 1}}}
    }}}`)
	if err != nil {
		t.Fatal(err)
	}
	if m.DefaultEffort() != "high" || m.Effort.ShowSummary() {
		t.Errorf("default=%q summary=%v", m.DefaultEffort(), m.Effort.ShowSummary())
	}
	if l, _ := m.EffortLevel("low"); l.Budget == nil || *l.Budget != 2048 {
		t.Errorf("low = %+v", l)
	}
	if l, _ := m.EffortLevel("off"); l.Budget == nil || *l.Budget != 0 {
		t.Errorf("off = %+v", l)
	}
	if l, _ := m.EffortLevel("medium"); !l.Empty() {
		t.Errorf("medium = %+v", l)
	}
	if l, _ := m.EffortLevel("high"); l.Effort != "high" || string(l.Body["x"]) != `{"y": 1}` {
		t.Errorf("high = %+v body=%s", l, l.Body["x"])
	}
}

func TestEffortValidation(t *testing.T) {
	cases := []struct {
		name, protocol, model, want string
	}{
		{"未知档位", ProtocolChat, `{"effort": {"levels": ["ultra"]}}`, `"ultra" 不是已知档位`},
		{"保留 auto", ProtocolChat, `{"effort": {"levels": ["auto"]}}`, "不能声明 auto"},
		{"空 levels", ProtocolChat, `{"effort": {"levels": []}}`, "levels 不能为空"},
		{"default 不存在", ProtocolChat, `{"effort": {"default": "max", "levels": ["low"]}}`, `.default "max" 不在 levels 里`},
		{"OpenAI 没有预算", ProtocolResponses, `{"effort": {"levels": {"low": 1024}}}`, "没有思考预算字段"},
		{"Anthropic 预算太小", ProtocolAnthropic, `{"effort": {"levels": {"low": 512}}}`, "至少 1024"},
		{"Anthropic 预算超过输出上限", ProtocolAnthropic, `{"maxOutput": 8000, "effort": {"levels": {"max": 8000}}}`, "必须小于 maxOutput"},
		{"Gemini 预算非法", ProtocolGoogle, `{"effort": {"levels": {"low": -2}}}`, "只能是 -1"},
		{"Gemini 强度预算并存", ProtocolGoogle, `{"effort": {"levels": {"low": {"effort": "low", "budget": 1024}}}}`, "不允许 thinkingLevel 与 thinkingBudget 同时出现"},
		{"对象字段拼错", ProtocolChat, `{"effort": {"levels": {"low": {"efort": "low"}}}}`, "只能包含 effort、budget、body"},
		{"空强度", ProtocolChat, `{"effort": {"levels": {"low": ""}}}`, "空字符串"},
		{"重复档位", ProtocolChat, `{"effort": {"levels": ["low", "low"]}}`, "写了两次"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := loadModel(t, c.protocol, c.model)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("错误 = %v，想要包含 %q", err, c.want)
			}
		})
	}
}

// 没有声明 effort 的模型只有 auto，不能切换档位。
func TestNoEffortDeclared(t *testing.T) {
	m, err := loadModel(t, ProtocolChat, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if m.EffortNames() != nil || m.DefaultEffort() != EffortAuto {
		t.Errorf("EffortNames=%v default=%q", m.EffortNames(), m.DefaultEffort())
	}
	if _, ok := m.EffortLevel(EffortAuto); !ok {
		t.Error("auto 应该总是可用")
	}
	if _, ok := m.EffortLevel("high"); ok {
		t.Error("没声明 effort 时 high 不该可用")
	}
}

func TestDescribeEffort(t *testing.T) {
	b := func(n int) *int { return &n }
	cases := []struct {
		protocol string
		l        EffortLevel
		summary  bool
		want     string
	}{
		{ProtocolChat, EffortLevel{}, true, "不发送思考参数"},
		{ProtocolChat, EffortLevel{Effort: "high"}, true, "reasoning_effort=high"},
		{ProtocolResponses, EffortLevel{Effort: "low"}, true, "reasoning.effort=low，summary=auto"},
		{ProtocolAnthropic, EffortLevel{Effort: "max"}, false, "thinking=adaptive，output_config.effort=max"},
		{ProtocolAnthropic, EffortLevel{Budget: b(4096)}, true, "thinking=enabled，budget_tokens=4096"},
		{ProtocolAnthropic, EffortLevel{Budget: b(0)}, true, "thinking=disabled"},
		{ProtocolGoogle, EffortLevel{Budget: b(-1)}, true, "thinkingBudget=-1（动态）"},
		{ProtocolChat, EffortLevel{Body: map[string]json.RawMessage{"thinking": json.RawMessage(`{ "type": "disabled" }`)}}, true, `thinking={"type":"disabled"}`},
	}
	for _, c := range cases {
		if got := DescribeEffort(c.protocol, c.l, c.summary); !strings.Contains(got, c.want) {
			t.Errorf("%s %+v = %q，想要包含 %q", c.protocol, c.l, got, c.want)
		}
	}
}
