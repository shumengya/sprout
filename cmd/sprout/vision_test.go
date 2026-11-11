package main

import (
	"strings"
	"testing"
)

// vision 配置要同时传到 tui.Info（决定是否提示“图片会被省略”）和 /model 切换后的结果。
func TestVisionFlagFollowsSelectedModel(t *testing.T) {
	dir := t.TempDir()
	a, err := buildApp(dir, testEnv(map[string]string{"DEEPSEEK_API_KEY": "k", "OPENAI_API_KEY": "k2"}, shellSet(true, true)), nil)
	if err != nil || a.ag == nil {
		t.Fatalf("装配失败: %v", err)
	}
	if a.info.Vision {
		t.Error("模板里 deepseek-flash 没有标注 vision，应为 false")
	}

	res, err := (modelStore{a}).Switch("openai/gpt-4o-mini")
	if err != nil {
		t.Fatal(err)
	}
	if !a.info.Vision || !res.Info.Vision {
		t.Error("切到标注了 vision 的 gpt-4o-mini 后应为 true")
	}

	if _, err := (modelStore{a}).Switch("deepseek/deepseek-flash"); err != nil {
		t.Fatal(err)
	}
	if a.info.Vision {
		t.Error("切回不支持视觉的模型后应恢复 false")
	}
}

func TestDescribeModelMentionsVision(t *testing.T) {
	dir := t.TempDir()
	a, err := buildApp(dir, testEnv(map[string]string{"DEEPSEEK_API_KEY": "k"}, shellSet(true, true)), nil)
	if err != nil {
		t.Fatal(err)
	}
	m := a.cfg.Providers["openai"].Models["gpt-4o-mini"]
	if got := describeModel(m); !strings.Contains(got, "支持图片") {
		t.Errorf("describeModel = %q", got)
	}
	d := a.cfg.Providers["deepseek"].Models["deepseek-flash"]
	if got := describeModel(d); strings.Contains(got, "支持图片") {
		t.Errorf("没有 vision 的模型不应标注: %q", got)
	}
}
