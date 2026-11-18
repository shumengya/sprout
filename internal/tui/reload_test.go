package tui

import (
	"testing"
)

// reloadModel 构造一个接了热加载的 Model；reloaded 记录 ReloadModels 被调用的次数。
func reloadModel(t *testing.T, models *fakeModels, reloaded *int) *Model {
	t.Helper()
	info := testInfo
	info.ContextWindow = 500000
	return newSized(t, Options{
		Runner: scriptRunner(), Models: models,
		ModelsChanged: func() bool { return false },
		ReloadModels: func() (Info, []string) {
			*reloaded++
			return info, []string{"已重新读取 models.jsonc（共 4 个模型）"}
		},
	}, 120, 40)
}

func TestModelsReloadWaitsUntilIdle(t *testing.T) {
	calls := 0
	m := reloadModel(t, testModels(), &calls)
	m.running = true // 生成中：不能替换 agent
	m.Update(modelsTickMsg{changed: true})
	if m.switching || !m.reloadDue {
		t.Fatalf("生成中只该记下待读取: switching=%v due=%v", m.switching, m.reloadDue)
	}

	m.running = false
	cmd := m.startReload()
	if cmd == nil || !m.switching || m.reloadDue {
		t.Fatalf("空闲后应开始读取，并按切换模型处理: cmd=%v switching=%v", cmd != nil, m.switching)
	}
	m.in.SetValue("读取期间发的消息")
	m.Update(keyEnter)
	if m.running {
		t.Error("读取期间不能发送，否则新提问会落进正被替换的 agent")
	}

	m.Update(cmd())
	if calls != 1 || m.switching || m.opts.Info.ContextWindow != 500000 {
		t.Fatalf("读取结束应刷新信息栏并回到空闲: calls=%d switching=%v window=%d", calls, m.switching, m.opts.Info.ContextWindow)
	}
	mustContain(t, plain(m), "已重新读取 models.jsonc")
}

func TestModelsTickWithoutChangeDoesNothing(t *testing.T) {
	calls := 0
	m := reloadModel(t, testModels(), &calls)
	if _, cmd := m.Update(modelsTickMsg{}); cmd == nil {
		t.Error("每次检查后都要约定下一次")
	}
	if m.switching || m.reloadDue || calls != 0 {
		t.Error("文件没改过不该读取")
	}
}

func TestModelsReloadRefreshesAnOpenPicker(t *testing.T) {
	calls := 0
	models := testModels()
	m := reloadModel(t, models, &calls)
	m.in.SetValue("/model")
	m.Update(keyEnter)
	if m.picker == nil {
		t.Fatal("应打开模型选择器")
	}
	m.Update(modelsTickMsg{changed: true})
	if !m.switching {
		t.Fatal("选择器开着也算空闲，应开始读取")
	}
	// 读取期间按 Enter 不能再起一次切换，两次替换 agent 会互相覆盖。
	m.Update(keyEnter)
	if m.picker == nil {
		t.Error("读取期间按 Enter 应保持选择器打开")
	}
	models.items = append(models.items, ModelChoice{Ref: "d/four", Provider: "d", Model: "four"})
	m.Update(modelsReloadedMsg{info: m.opts.Info})
	if m.picker == nil || len(m.picker.items) != 4 {
		t.Errorf("选择器应换上新的模型列表: %+v", m.picker)
	}
}

func TestNoModelsWatchWithoutReloader(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner(), ModelsChanged: func() bool { return true }}, 120, 40)
	if m.modelsWatchCmd() != nil {
		t.Error("没有 ReloadModels 时不该检查")
	}
}
