package main

import (
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent/config"
)

// effortConfig 有三个模型：chat 用 body 关思考、anthropic 走 adaptive、plain 没有声明 effort。
const effortConfig = `{
  "default": "ds/flash",
  "providers": {
    "ds": {"protocol": "openai-chat", "baseUrl": "https://x.test", "apiKey": "k", "models": {
      "flash": {"effort": {"default": "high", "levels": {
        "off": {"body": {"thinking": {"type": "disabled"}}},
        "low": "low", "high": "high", "max": "max"
      }}},
      "plain": {}
    }},
    "claude": {"protocol": "anthropic-messages", "baseUrl": "https://x.test/v1", "apiKey": "k", "models": {
      "opus": {"maxOutput": 64000, "effort": {"levels": ["low", "medium", "high", "xhigh", "max"]}}
    }}
  }
}`

func effortApp(t *testing.T, flag string) (*app, error) {
	t.Helper()
	dir := t.TempDir()
	writeConfig(t, dir, effortConfig)
	e := testEnv(nil, shellSet(true, true))
	e.effort = flag
	return buildApp(dir, e, nil)
}

func TestStartupEffortFromConfigAndFlag(t *testing.T) {
	a, err := effortApp(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if a.effort != "high" || a.info.Effort != "high" {
		t.Errorf("默认档位 effort=%q info=%q", a.effort, a.info.Effort)
	}
	if th := a.ag.Thinking(); th.Effort != "high" || th.Level != "high" {
		t.Errorf("agent 思考参数 = %+v", th)
	}

	a, err = effortApp(t, "OFF") // 大小写不敏感
	if err != nil {
		t.Fatal(err)
	}
	th := a.ag.Thinking()
	if a.effort != "off" || th.Active() || string(th.Extra["thinking"]) != `{"type": "disabled"}` {
		t.Errorf("--effort off：effort=%q thinking=%+v extra=%s", a.effort, th, th.Extra["thinking"])
	}

	if _, err := effortApp(t, "xhigh"); err == nil || !strings.Contains(err.Error(), "auto、off、low、high、max") {
		t.Errorf("不存在的档位应报错并列出可选项，得到 %v", err)
	}
}

func TestEffortFlagOnModelWithoutEffort(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, strings.Replace(effortConfig, `"default": "ds/flash"`, `"default": "ds/plain"`, 1))
	e := testEnv(nil, shellSet(true, true))
	a, err := buildApp(dir, e, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.info.Effort != "" || a.ag.Thinking().Active() {
		t.Errorf("没声明 effort 的模型不应显示档位或发参数: info=%q", a.info.Effort)
	}
	s := effortStore{a}
	if !strings.Contains(s.Unsupported(), "ds/plain") || len(s.Choices()) != 0 {
		t.Errorf("Unsupported = %q，Choices = %v", s.Unsupported(), s.Choices())
	}

	e.effort = "high"
	if _, err := buildApp(dir, e, nil); err == nil || !strings.Contains(err.Error(), "没有在 models.jsonc 声明 effort") {
		t.Errorf("对不支持的模型用 --effort 应报错，得到 %v", err)
	}
}

func TestEffortStoreSet(t *testing.T) {
	a, err := effortApp(t, "")
	if err != nil {
		t.Fatal(err)
	}
	s := effortStore{a}

	choices := s.Choices()
	var names []string
	for _, c := range choices {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "auto,off,low,high,max" {
		t.Errorf("档位 = %v", names)
	}
	if !strings.Contains(choices[0].Detail, "不发送") || !strings.Contains(choices[1].Detail, `thinking={"type":"disabled"}`) ||
		choices[4].Detail != "reasoning_effort=max" {
		t.Errorf("说明 = %+v", choices)
	}

	notice, err := s.Set("max")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notice, "high → max") || a.ag.Thinking().Effort != "max" || a.info.Effort != "max" || s.Current() != "max" {
		t.Errorf("Set(max)：notice=%q thinking=%+v info=%q", notice, a.ag.Thinking(), a.info.Effort)
	}
	if _, err := s.Set("auto"); err != nil || a.ag.Thinking().Active() || a.ag.Thinking().Extra != nil {
		t.Errorf("auto 应清空全部思考参数：err=%v thinking=%+v", err, a.ag.Thinking())
	}
	if _, err := s.Set("xhigh"); err == nil {
		t.Error("不存在的档位应报错")
	}
	if s.Current() != "auto" {
		t.Errorf("失败的 Set 不应改变档位: %q", s.Current())
	}
}

// 切模型：新模型有同名档位就沿用，没有就退回它的默认档位；不支持的模型不显示档位。
func TestSwitchModelCarriesEffort(t *testing.T) {
	a, err := effortApp(t, "")
	if err != nil {
		t.Fatal(err)
	}
	ms := modelStore{a}

	res, err := ms.Switch("claude/opus") // high 在 opus 里也有
	if err != nil {
		t.Fatal(err)
	}
	if a.effort != "high" || res.Info.Effort != "high" || !strings.Contains(res.Notice, "思考程度 high") {
		t.Errorf("沿用档位：effort=%q info=%q notice=%q", a.effort, res.Info.Effort, res.Notice)
	}
	if th := a.ag.Thinking(); th.Effort != "high" || !th.Summary {
		t.Errorf("新 agent 的思考参数 = %+v", th)
	}

	if _, err := (effortStore{a}).Set("xhigh"); err != nil {
		t.Fatal(err)
	}
	res, err = ms.Switch("ds/flash") // flash 没有 xhigh，退回它的默认 high
	if err != nil {
		t.Fatal(err)
	}
	if a.effort != "high" || !strings.Contains(res.Notice, "xhigh → high") {
		t.Errorf("退回默认：effort=%q notice=%q", a.effort, res.Notice)
	}

	res, err = ms.Switch("ds/plain")
	if err != nil {
		t.Fatal(err)
	}
	if res.Info.Effort != "" || a.ag.Thinking().Active() || strings.Contains(res.Notice, "思考程度") {
		t.Errorf("不支持的模型：info=%q thinking=%+v notice=%q", res.Info.Effort, a.ag.Thinking(), res.Notice)
	}
	if a.effort != config.EffortAuto {
		t.Errorf("不支持的模型档位应为 auto，得到 %q", a.effort)
	}
}

func TestParseArgsEffort(t *testing.T) {
	for _, argv := range [][]string{
		{"--effort", "max"},
		{"--effort=max", "run", "q"},
		{"run", "--effort", "max", "q"},
	} {
		a, err := parseArgs(argv)
		if err != nil || a.effort != "max" {
			t.Errorf("%v → effort=%q err=%v", argv, a.effort, err)
		}
	}
	if !strings.Contains(usage, "--effort") {
		t.Error("用法说明缺少 --effort")
	}
}
