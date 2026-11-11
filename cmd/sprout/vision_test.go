package main

import (
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent/config"
)

// 模板里所有模型都标了 vision，要测“不支持视觉”只能自己改出一个：去掉 gpt-4o-mini 的 vision。
func configWithoutOpenAIVision(t *testing.T) string {
	t.Helper()
	old := `"gpt-4o-mini": {
          "contextWindow": 128000,
          "maxOutput": 16384,
          "tools": true,
          "vision": true
        }`
	if !strings.Contains(config.Template, old) {
		t.Fatal("config.Template 里 gpt-4o-mini 的写法变了，需要同步这里的匹配串")
	}
	return strings.Replace(config.Template, old, `"gpt-4o-mini": {
          "contextWindow": 128000,
          "maxOutput": 16384,
          "tools": true
        }`, 1)
}

// vision 配置要同时传到 tui.Info（决定是否提示“图片会被省略”）和 /model 切换后的结果。
func TestVisionFlagFollowsSelectedModel(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, configWithoutOpenAIVision(t))
	a, err := buildApp(dir, testEnv(map[string]string{"DEEPSEEK_API_KEY": "k", "OPENAI_API_KEY": "k2"}, shellSet(true, true)), nil)
	if err != nil || a.ag == nil {
		t.Fatalf("装配失败: %v", err)
	}
	if !a.info.Vision {
		t.Error("默认的 deepseek-flash 标注了 vision，应为 true")
	}

	res, err := (modelStore{a}).Switch("openai/gpt-4o-mini")
	if err != nil {
		t.Fatal(err)
	}
	if a.info.Vision || res.Info.Vision {
		t.Error("切到未标注 vision 的模型后应为 false")
	}

	if _, err := (modelStore{a}).Switch("deepseek/deepseek-flash"); err != nil {
		t.Fatal(err)
	}
	if !a.info.Vision {
		t.Error("切回支持视觉的模型后应恢复 true")
	}
}

func TestDescribeModelMentionsVision(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, configWithoutOpenAIVision(t))
	a, err := buildApp(dir, testEnv(map[string]string{"DEEPSEEK_API_KEY": "k"}, shellSet(true, true)), nil)
	if err != nil {
		t.Fatal(err)
	}
	d := a.cfg.Providers["deepseek"].Models["deepseek-flash"]
	if got := describeModel(d); !strings.Contains(got, "支持图片") {
		t.Errorf("describeModel = %q", got)
	}
	m := a.cfg.Providers["openai"].Models["gpt-4o-mini"]
	if got := describeModel(m); strings.Contains(got, "支持图片") {
		t.Errorf("没有 vision 的模型不应标注: %q", got)
	}
}
