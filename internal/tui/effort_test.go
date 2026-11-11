package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// fakeEfforts 是可编程的档位替身。只用于测试。
type fakeEfforts struct {
	items  []EffortChoice
	cur    string
	setErr error
	sets   []string
}

func (f *fakeEfforts) Choices() []EffortChoice { return f.items }
func (f *fakeEfforts) Current() string         { return f.cur }
func (f *fakeEfforts) Unsupported() string     { return "模型 x 没有声明 effort" }
func (f *fakeEfforts) Set(name string) (string, error) {
	f.sets = append(f.sets, name)
	if f.setErr != nil {
		return "", f.setErr
	}
	old := f.cur
	f.cur = name
	return "思考程度：" + old + " → " + name, nil
}

func testEfforts() *fakeEfforts {
	return &fakeEfforts{cur: "high", items: []EffortChoice{
		{Name: "auto", Detail: "不发送思考参数"},
		{Name: "low", Detail: "reasoning_effort=low"},
		{Name: "high", Detail: "reasoning_effort=high"},
		{Name: "max", Detail: "reasoning_effort=max"},
	}}
}

func effortModel(t *testing.T, e Efforts) *Model {
	info := testInfo
	info.Effort = "high"
	return newSized(t, Options{Runner: scriptRunner(), Info: info, Efforts: e}, 100, 30)
}

func TestEffortPickerSelectsWithArrows(t *testing.T) {
	e := testEfforts()
	m := effortModel(t, e)

	submit(m, "/effort")
	if m.effort == nil || m.effort.cursor != 2 {
		t.Fatalf("应打开档位条并停在当前档位: %+v", m.effort)
	}
	screen := plain(m)
	mustContain(t, screen, "思考程度", "auto", "low", "high", "max", "reasoning_effort=high", "← 当前", "更快 → 更深入")
	if len(strings.Split(m.View().Content, "\n")) != 30 {
		t.Fatal("打开档位条后画面高度应保持不变")
	}

	m.Update(press(tea.KeyRight, 0))
	mustContain(t, plain(m), "reasoning_effort=max")
	m.Update(press(tea.KeyRight, 0)) // 已在最右，不越界
	if m.effort.cursor != 3 {
		t.Fatalf("cursor = %d", m.effort.cursor)
	}
	m.Update(keyEnter)
	if m.effort != nil {
		t.Fatal("回车后档位条应关闭")
	}
	if len(e.sets) != 1 || e.sets[0] != "max" || m.opts.Info.Effort != "max" {
		t.Fatalf("sets=%v info=%q", e.sets, m.opts.Info.Effort)
	}
	mustContain(t, plain(m), "思考程度：high → max", "思考 max")
}

func TestEffortPickerDigitsEscapeAndUnchanged(t *testing.T) {
	e := testEfforts()
	m := effortModel(t, e)

	submit(m, "/effort")
	m.Update(tea.KeyPressMsg{Code: '1', Text: "1"})
	if m.effort.cursor != 0 {
		t.Fatalf("数字键应跳到对应档位，cursor = %d", m.effort.cursor)
	}
	m.Update(tea.KeyPressMsg{Code: '9', Text: "9"}) // 超出范围：不动
	if m.effort.cursor != 0 {
		t.Fatalf("超出范围的数字不应移动，cursor = %d", m.effort.cursor)
	}
	m.Update(press(tea.KeyEscape, 0))
	if m.effort != nil || len(e.sets) != 0 {
		t.Fatal("Esc 应关闭且不切换")
	}
	mustContain(t, plain(m), "已取消调整思考程度")

	// 回车选中当前档位：不调用 Set。
	submit(m, "/effort")
	m.Update(keyEnter)
	if len(e.sets) != 0 {
		t.Fatalf("选中当前档位不应切换: %v", e.sets)
	}
	mustContain(t, plain(m), "思考程度保持 high")
}

func TestEffortDirectSetAndErrors(t *testing.T) {
	e := testEfforts()
	m := effortModel(t, e)
	submit(m, "/effort LOW")
	if m.effort != nil || len(e.sets) != 1 || e.sets[0] != "low" {
		t.Fatalf("带参数应直接切换（大小写不敏感）: sets=%v", e.sets)
	}

	e.setErr = errors.New(`没有 "ultra" 档位`)
	submit(m, "/effort ultra")
	mustContain(t, plain(m), "调整思考程度失败", `没有 "ultra" 档位`)
	if m.opts.Info.Effort != "low" {
		t.Errorf("失败后信息栏不应变化: %q", m.opts.Info.Effort)
	}
}

// 生成中也能调：只改下一次请求的参数，不打断当前这一轮。
func TestEffortWorksWhileRunning(t *testing.T) {
	e := testEfforts()
	m := effortModel(t, e)
	m.running = true
	submit(m, "/effort max")
	if len(e.sets) != 1 || !m.running {
		t.Fatalf("生成中应能切换且不影响当前一轮: sets=%v running=%v", e.sets, m.running)
	}
}

func TestEffortUnsupportedAndUnavailable(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner(), Efforts: &fakeEfforts{}}, 100, 30)
	submit(m, "/effort")
	if m.effort != nil {
		t.Fatal("不支持的模型不应打开档位条")
	}
	mustContain(t, plain(m), "模型 x 没有声明 effort")

	m = newSized(t, Options{Runner: scriptRunner()}, 100, 30)
	submit(m, "/effort")
	mustContain(t, plain(m), "当前不能调整思考程度")
}

func TestInfoLineShowsEffortOnlyWhenDeclared(t *testing.T) {
	m := effortModel(t, testEfforts())
	mustContain(t, plain(m), "思考 high")

	m = newSized(t, Options{Runner: scriptRunner()}, 100, 30)
	if strings.Contains(plain(m), "思考 ") {
		t.Error("没声明 effort 时信息栏不应显示档位")
	}
	submit(m, "/help")
	mustContain(t, plain(m), "/effort")
}

// 切换模型后信息栏的档位跟着新模型变。
func TestModelSwitchUpdatesEffort(t *testing.T) {
	models := testModels()
	models.res = &SwitchResult{Info: Info{Model: "two", Provider: "b", Effort: "medium"}, Notice: "已切换"}
	m := newSized(t, Options{Runner: scriptRunner(), Models: models, Efforts: testEfforts()}, 100, 30)
	m.in.SetValue("/model b/two")
	_, cmd := m.Update(keyEnter)
	runCmd(m, cmd)
	if m.opts.Info.Effort != "medium" {
		t.Errorf("Info.Effort = %q", m.opts.Info.Effort)
	}
	mustContain(t, plain(m), "思考 medium")
}
