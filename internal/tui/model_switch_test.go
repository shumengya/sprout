package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// fakeModels 是可编程的模型切换替身。只用于测试。
type fakeModels struct {
	items     []ModelChoice
	current   string
	switchErr error
	gotRef    string
	res       *SwitchResult
}

func (f *fakeModels) List() []ModelChoice { return f.items }
func (f *fakeModels) Current() string     { return f.current }
func (f *fakeModels) Switch(ref string) (*SwitchResult, error) {
	f.gotRef = ref
	if f.switchErr != nil {
		return nil, f.switchErr
	}
	if f.res != nil {
		return f.res, nil
	}
	return &SwitchResult{Info: Info{Model: ref, Provider: "p"}, Notice: "已切换：" + ref}, nil
}

func testModels() *fakeModels {
	return &fakeModels{
		current: "a/one",
		items: []ModelChoice{
			{Ref: "a/one", Provider: "a", Model: "one", Protocol: "openai-chat", Detail: "上下文 128k", Current: true},
			{Ref: "b/two", Provider: "b", Model: "two", Protocol: "anthropic-messages"},
			{Ref: "c/three", Provider: "c", Model: "three", Protocol: "google-generate"},
		},
	}
}

func TestModelPickerOpensAndSelects(t *testing.T) {
	models := testModels()
	m := newSized(t, Options{Runner: scriptRunner(), Models: models}, 100, 40)

	submit(m, "/model")
	if m.picker == nil {
		t.Fatal("/model 应打开选择器")
	}
	screen := plain(m)
	mustContain(t, screen, "选择模型", "a/one", "b/two", "c/three", "← 当前")

	// 下移一项后回车：切到 b/two。
	m.Update(press(tea.KeyDown, 0))
	_, cmd := m.Update(keyEnter)
	if cmd == nil {
		t.Fatal("回车应返回切换命令")
	}
	if m.picker != nil {
		t.Fatal("选中后选择器应关闭")
	}
	runCmd(m, cmd) // 执行异步切换并把结果喂回
	if models.gotRef != "b/two" {
		t.Fatalf("切换目标 = %q", models.gotRef)
	}
	if m.opts.Info.Model != "b/two" {
		t.Errorf("信息栏模型未更新: %q", m.opts.Info.Model)
	}
	mustContain(t, plain(m), "已切换：b/two")
	if m.switching {
		t.Error("切换完成后不应仍在切换中")
	}
}

func TestModelPickerFilterByProtocol(t *testing.T) {
	models := testModels()
	m := newSized(t, Options{Runner: scriptRunner(), Models: models}, 100, 40)
	submit(m, "/model")
	for _, r := range "anthropic" { // 按协议名过滤
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	v := m.picker.visible()
	if len(v) != 1 || m.picker.items[v[0]].Ref != "b/two" {
		t.Fatalf("过滤结果 = %v", v)
	}
	_, cmd := m.Update(keyEnter)
	runCmd(m, cmd)
	if models.gotRef != "b/two" {
		t.Fatalf("应按协议过滤后选中 b/two，得到 %q", models.gotRef)
	}
}

func TestModelPickerEscapeCancels(t *testing.T) {
	models := testModels()
	m := newSized(t, Options{Runner: scriptRunner(), Models: models}, 100, 40)
	submit(m, "/model")
	m.Update(press(tea.KeyEscape, 0))
	if m.picker != nil {
		t.Fatal("Esc 应关闭选择器")
	}
	if models.gotRef != "" {
		t.Fatal("取消不应触发切换")
	}
	mustContain(t, plain(m), "已取消选择模型")
}

func TestModelDirectSwitchByRef(t *testing.T) {
	models := testModels()
	m := newSized(t, Options{Runner: scriptRunner(), Models: models}, 100, 40)
	m.in.SetValue("/model c/three")
	_, cmd := m.Update(keyEnter)
	if m.picker != nil {
		t.Fatal("带引用的 /model 不应打开选择器")
	}
	runCmd(m, cmd)
	if models.gotRef != "c/three" {
		t.Fatalf("切换目标 = %q", models.gotRef)
	}
}

func TestModelSwitchRefusedWhileRunning(t *testing.T) {
	models := testModels()
	m := newSized(t, Options{Runner: scriptRunner(), Models: models}, 100, 40)
	m.running = true
	submit(m, "/model")
	if m.picker != nil || models.gotRef != "" {
		t.Fatal("生成中不应打开选择器或切换")
	}
	mustContain(t, plain(m), "暂时不能切换模型")
}

func TestModelSwitchErrorIsShown(t *testing.T) {
	models := testModels()
	models.switchErr = errors.New("未配置 API Key")
	m := newSized(t, Options{Runner: scriptRunner(), Models: models}, 100, 40)
	m.in.SetValue("/model b/two")
	_, cmd := m.Update(keyEnter)
	runCmd(m, cmd)
	mustContain(t, plain(m), "切换模型失败", "未配置 API Key")
	if m.opts.Info.Model != testInfo.Model {
		t.Errorf("失败后信息栏不应变化: %q", m.opts.Info.Model)
	}
	if m.switching {
		t.Error("失败后不应停在切换中")
	}
}

func TestModelCommandWithoutModels(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 40)
	submit(m, "/model")
	mustContain(t, plain(m), "当前不能切换模型")
}

func TestSubmitBlockedWhileSwitching(t *testing.T) {
	models := testModels()
	m := newSized(t, Options{Runner: scriptRunner(), Models: models}, 100, 40)
	m.switching = true
	submit(m, "你好")
	if m.running {
		t.Fatal("切换模型期间不应启动对话")
	}
	mustContain(t, plain(m), "正在切换模型")
}

func TestModelPickerKeepsLayoutHeight(t *testing.T) {
	models := testModels()
	m := newSized(t, Options{Runner: scriptRunner(), Models: models}, 100, 40)
	submit(m, "/model")
	lines := strings.Split(m.View().Content, "\n")
	if len(lines) != 40 {
		t.Fatalf("打开选择器后画面 %d 行，应仍为 40 行", len(lines))
	}
}
