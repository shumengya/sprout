package main

import (
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/config"
)

// 切模型必须保留对话历史，且新模型的信息同步到 tui.Info。
func TestSwitchModelKeepsHistoryAndUpdatesInfo(t *testing.T) {
	dir := t.TempDir()
	a, err := buildApp(dir, testEnv(map[string]string{"DEEPSEEK_API_KEY": "k"}, shellSet(true, true)), nil)
	if err != nil || a.ag == nil {
		t.Fatalf("装配失败: %v", err)
	}
	a.ag.Restore([]agent.Message{
		{Role: "user", Content: []agent.Block{agent.TextBlock("之前的问题")}},
		{Role: "assistant", Content: []agent.Block{agent.TextBlock("之前的回答")}, Reasoning: "别家的思维链"},
	}, "")

	store := modelStore{a}
	res, err := store.Switch("deepseek-responses/deepseek-flash")
	if err != nil {
		t.Fatal(err)
	}
	if a.sel.Ref() != "deepseek-responses/deepseek-flash" {
		t.Errorf("sel = %s", a.sel.Ref())
	}
	if res != nil {
		if !strings.Contains(res.Notice, "deepseek-responses/deepseek-flash") {
			t.Errorf("提示 = %q", res.Notice)
		}
		if res.Info.Model != "deepseek-flash" || !strings.Contains(res.Info.Provider, config.ProtocolResponses) {
			t.Errorf("Info = %+v", res.Info)
		}
	}

	h := a.ag.History()
	if len(h) != 2 || h[0].Text() != "之前的问题" || h[1].Text() != "之前的回答" {
		t.Fatalf("历史没保留: %+v", h)
	}
	// 换模型要清掉思维链：别家的 reasoning 不该回传。
	if h[1].Reasoning != "" {
		t.Error("切换模型后应清空思维链")
	}
}

// 切到不存在的模型、或切到当前模型，都应报错且不改变现状。
func TestSwitchModelErrorsKeepCurrent(t *testing.T) {
	a, err := buildApp(t.TempDir(), testEnv(map[string]string{"DEEPSEEK_API_KEY": "k"}, shellSet(true, true)), nil)
	if err != nil || a.ag == nil {
		t.Fatalf("装配失败: %v", err)
	}
	store := modelStore{a}
	before := a.ag

	if _, err := store.Switch("nope/nope"); err == nil {
		t.Error("不存在的 provider 应报错")
	}
	if _, err := store.Switch("deepseek/deepseek-flash"); err == nil {
		t.Error("切到当前模型应报错")
	}
	// 没有 Key 的 provider 也要失败，且不能把 app 弄成半切换状态。
	if _, err := store.Switch("openai/gpt-4o-mini"); err == nil {
		t.Error("缺少 API Key 应报错")
	}
	if a.ag != before || a.sel.Ref() != "deepseek/deepseek-flash" {
		t.Errorf("失败后 agent 或模型被改动: %s", a.sel.Ref())
	}
}

func TestModelListCoversConfig(t *testing.T) {
	a, err := buildApp(t.TempDir(), testEnv(map[string]string{"DEEPSEEK_API_KEY": "k"}, shellSet(true, true)), nil)
	if err != nil {
		t.Fatal(err)
	}
	list := modelStore{a}.List()
	if len(list) != len(a.cfg.All()) {
		t.Fatalf("列表 %d 项，配置 %d 项", len(list), len(a.cfg.All()))
	}
	current := 0
	for _, it := range list {
		if it.Current {
			current++
			if it.Ref != "deepseek/deepseek-flash" {
				t.Errorf("当前模型标记错误: %s", it.Ref)
			}
		}
		if it.Protocol == "" || it.Detail == "" {
			t.Errorf("列表项缺少信息: %+v", it)
		}
	}
	if current != 1 {
		t.Errorf("应恰好有一个当前模型，得到 %d", current)
	}
}

// tools=false 的模型切换后不提供工具。
func TestSwitchToToolLessModelDropsTools(t *testing.T) {
	dir := t.TempDir()
	cfg := strings.Replace(config.Template, `"default": "deepseek/deepseek-flash"`, `"default": "deepseek/deepseek-flash"`, 1)
	cfg = strings.Replace(cfg, `"openai": {
      "protocol": "openai-chat",
      "baseUrl": "https://api.openai.com/v1",
      "apiKeyEnv": "OPENAI_API_KEY",
      "models": {
        "gpt-4o-mini": {
          "contextWindow": 128000,
          "maxOutput": 16384,
          "tools": true,
          "vision": true
        }
      }
    }`, `"openai": {
      "protocol": "openai-chat",
      "baseUrl": "https://api.openai.com/v1",
      "apiKeyEnv": "OPENAI_API_KEY",
      "apiKey": "k2",
      "models": {
        "gpt-4o-mini": {
          "contextWindow": 128000,
          "maxOutput": 16384,
          "tools": false
        }
      }
    }`, 1)
	if !strings.Contains(cfg, `"apiKey": "k2"`) {
		t.Fatal("替换没生效：config.Template 里 openai 段的写法变了，需要同步这里的匹配串")
	}
	writeConfig(t, dir, cfg)
	a, err := buildApp(dir, testEnv(map[string]string{"DEEPSEEK_API_KEY": "k"}, shellSet(true, true)), nil)
	if err != nil || a.ag == nil {
		t.Fatalf("装配失败: %v", err)
	}
	if _, err := (modelStore{a}).Switch("openai/gpt-4o-mini"); err != nil {
		t.Fatal(err)
	}
	if len(a.info.Tools) != 0 || len(a.reg.Names()) != 0 {
		t.Errorf("tools=false 的模型不应提供工具: info=%v reg=%v", a.info.Tools, a.reg.Names())
	}
}
