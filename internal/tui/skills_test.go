package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/event"
)

// fakeSkills 记录面板发来的操作，返回固定的技能列表。
type fakeSkills struct {
	skills     []SkillInfo
	scanAgents bool
	calls      []string
	err        error
	notice     string
}

func (f *fakeSkills) Skills() []SkillInfo { return f.skills }

func (f *fakeSkills) SetSkill(name string, enable bool) (string, error) {
	f.calls = append(f.calls, "skill:"+name+":"+boolTag(enable))
	return f.notice, f.err
}

func (f *fakeSkills) Rescan() (string, error) {
	f.calls = append(f.calls, "rescan")
	return f.notice, f.err
}

func (f *fakeSkills) ScanAgents() bool { return f.scanAgents }

func (f *fakeSkills) SetScanAgents(on bool) (string, error) {
	f.scanAgents = on
	f.calls = append(f.calls, "agents:"+boolTag(on))
	return f.notice, f.err
}

// Trigger 记录调用，返回一段可辨认的正文；名字不在列表里时按“不是技能”处理。
func (f *fakeSkills) Trigger(name, task string) (string, error) {
	f.calls = append(f.calls, "trigger:"+name+":"+task)
	if f.err != nil {
		return "", f.err
	}
	for _, sk := range f.skills {
		if sk.Name == name {
			if !sk.Enabled {
				return "", errors.New("技能 " + name + " 已停用")
			}
			return "SKILL BODY of " + name + "\n任务：" + task, nil
		}
	}
	return "", ErrUnknownSkill
}

func skillList() []SkillInfo {
	return []SkillInfo{
		{Name: "lark", Desc: "飞书文档读写", Source: "项目", Enabled: true},
		{Name: "pdf", Desc: "PDF 提取文本", Source: "用户", Enabled: false},
	}
}

// openSkills 打开 /skills 面板。
func openSkills(t *testing.T, f *fakeSkills) *Model {
	t.Helper()
	m := newSized(t, Options{Runner: scriptRunner(), Info: Info{Skills: f.Skills()}, Skills: f}, 120, 40)
	m.in.SetValue("/skills")
	m.Update(keyEnter)
	if m.skillPick == nil {
		t.Fatal("/skills 应打开面板")
	}
	return m
}

func TestSkillsPanelListsSkills(t *testing.T) {
	m := openSkills(t, &fakeSkills{skills: skillList()})
	mustContain(t, plain(m), "技能", "已启用 1/2", "lark", "飞书文档读写", "pdf", "PDF 提取文本",
		"项目", "用户", "空格 / Enter 开关", "r 重新扫描", "Esc 关闭")
}

func TestSkillsPanelTogglesSkill(t *testing.T) {
	f := &fakeSkills{skills: skillList(), notice: "已停用技能 lark，下一轮对话生效"}
	m := openSkills(t, f)
	_, cmd := m.Update(typed(" "))
	if m.skillPick.busy == "" {
		t.Error("操作期间面板应标记为忙")
	}
	runCmd(m, cmd)
	if len(f.calls) != 1 || f.calls[0] != "skill:lark:off" {
		t.Fatalf("应请求停用 lark，得到 %v", f.calls)
	}
	if m.skillPick.busy != "" || m.skillPick.note != "已停用技能 lark，下一轮对话生效" {
		t.Errorf("完成后应显示结果: busy=%q note=%q", m.skillPick.busy, m.skillPick.note)
	}
}

// 停用的技能再按一次应该启用。
func TestSkillsPanelEnablesDisabledSkill(t *testing.T) {
	f := &fakeSkills{skills: skillList()}
	m := openSkills(t, f)
	m.Update(press(tea.KeyDown, 0))
	_, cmd := m.Update(keyEnter)
	runCmd(m, cmd)
	if len(f.calls) != 1 || f.calls[0] != "skill:pdf:on" {
		t.Errorf("应启用 pdf，得到 %v", f.calls)
	}
}

func TestSkillsPanelRescan(t *testing.T) {
	f := &fakeSkills{skills: skillList(), notice: "已重新扫描技能：共 2 个，启用 1 个"}
	m := openSkills(t, f)
	_, cmd := m.Update(typed("r"))
	runCmd(m, cmd)
	if len(f.calls) != 1 || f.calls[0] != "rescan" {
		t.Fatalf("r 应触发重新扫描，得到 %v", f.calls)
	}
	if !strings.Contains(plain(m), "已重新扫描技能") {
		t.Errorf("应显示结果:\n%s", plain(m))
	}
}

func TestSkillsPanelShowsFailure(t *testing.T) {
	f := &fakeSkills{skills: skillList(), err: errors.New("skills.jsonc 不是合法 JSONC")}
	m := openSkills(t, f)
	_, cmd := m.Update(typed(" "))
	runCmd(m, cmd)
	if !strings.Contains(plain(m), "不是合法 JSONC") {
		t.Errorf("失败原因应显示在面板里:\n%s", plain(m))
	}
}

func TestSkillsPanelBusyIgnoresToggle(t *testing.T) {
	f := &fakeSkills{skills: skillList()}
	m := openSkills(t, f)
	m.skillPick.busy = "正在重新扫描技能目录…"
	if _, cmd := m.Update(typed(" ")); cmd != nil {
		t.Error("忙时不该再发操作")
	}
	if len(f.calls) != 0 {
		t.Errorf("忙时不该调用实现，得到 %v", f.calls)
	}
}

func TestSkillsPanelEscCloses(t *testing.T) {
	m := openSkills(t, &fakeSkills{skills: skillList()})
	m.Update(keyEsc)
	if m.skillPick != nil {
		t.Error("Esc 应关闭面板")
	}
	if !strings.Contains(plain(m), "已关闭技能面板") {
		t.Errorf("应有提示:\n%s", plain(m))
	}
}

func TestSkillsPanelRefusesWhileRunning(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner(), Info: Info{Skills: skillList()}, Skills: &fakeSkills{}}, 120, 40)
	m.running = true
	m.in.SetValue("/skills")
	m.Update(keyEnter)
	if m.skillPick != nil {
		t.Error("生成中不该打开面板")
	}
	mustContain(t, plain(m), "暂时不能管理技能")
}

func TestSkillsPanelWithoutImplementation(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 120, 40)
	submit(m, "/skills")
	if m.skillPick != nil {
		t.Error("没有实现时不该打开面板")
	}
	mustContain(t, plain(m), "当前不能管理技能")
}

func TestSkillsPanelEmptyList(t *testing.T) {
	m := openSkills(t, &fakeSkills{})
	mustContain(t, plain(m), "没有发现技能")
	// 空列表里按键不应 panic。
	for _, k := range []tea.KeyPressMsg{press(tea.KeyDown, 0), typed(" "), typed("r")} {
		if _, cmd := m.Update(k); cmd != nil {
			runCmd(m, cmd)
		}
	}
}

// a 键切换 .agents/skills 的自动加载，面板上的状态跟着变。
func TestSkillsPanelTogglesScanAgents(t *testing.T) {
	f := &fakeSkills{skills: skillList(), scanAgents: true}
	m := openSkills(t, f)
	mustContain(t, plain(m), ".agents 自动加载：开")

	_, cmd := m.Update(typed("a"))
	runCmd(m, cmd)
	if len(f.calls) != 1 || f.calls[0] != "agents:off" {
		t.Fatalf("a 应关闭自动加载，得到 %v", f.calls)
	}
	mustContain(t, plain(m), ".agents 自动加载：关")

	// 面板关掉再打开，状态从实现里重新取。
	m.Update(keyEsc)
	m.in.SetValue("/skills")
	m.Update(keyEnter)
	mustContain(t, plain(m), ".agents 自动加载：关")
}

// /技能名 任务 手动触发：发出去的是技能正文 + 任务，对话区保留用户的原始输入。
func TestSkillTriggerSendsInvocation(t *testing.T) {
	got := make(chan agent.Input, 1)
	r := fakeRunner{run: func(_ context.Context, in agent.Input, sink event.Sink) error {
		got <- in
		sink.Emit(event.TurnFinished{Reason: event.ReasonStop})
		return nil
	}}
	f := &fakeSkills{skills: skillList()}
	m := newSized(t, Options{Runner: r, Info: Info{Skills: f.Skills()}, Skills: f}, 120, 40)
	m.in.SetValue("/lark 整理这份会议记录")
	runProgram(t, m, func(send func(tea.Msg)) { send(keyEnter) }, finishedOneTurn)

	select {
	case in := <-got:
		if !strings.Contains(in.Text, "SKILL BODY of lark") || !strings.Contains(in.Text, "整理这份会议记录") {
			t.Errorf("发出的正文 = %q", in.Text)
		}
	default:
		t.Fatal("应发起一轮请求")
	}
	if len(f.calls) != 1 || f.calls[0] != "trigger:lark:整理这份会议记录" {
		t.Errorf("calls = %v", f.calls)
	}
	// 对话区显示用户原来的 /lark ...，而不是展开后的整段正文。
	var userText string
	for _, e := range m.entries {
		if e.kind == kindUser {
			userText = e.text
		}
	}
	if userText != "/lark 整理这份会议记录" {
		t.Errorf("对话区 = %q", userText)
	}
	mustContain(t, plain(m), "已手动触发技能 lark")
}

// 名字对不上技能时整段当作普通提问，/etc/hosts 这类输入不受影响。
func TestUnknownSlashFallsThroughToPrompt(t *testing.T) {
	got := make(chan agent.Input, 1)
	r := fakeRunner{run: func(_ context.Context, in agent.Input, sink event.Sink) error {
		got <- in
		sink.Emit(event.TurnFinished{Reason: event.ReasonStop})
		return nil
	}}
	f := &fakeSkills{skills: skillList()}
	m := newSized(t, Options{Runner: r, Info: Info{Skills: f.Skills()}, Skills: f}, 120, 40)
	m.in.SetValue("/etc/hosts 是什么")
	runProgram(t, m, func(send func(tea.Msg)) { send(keyEnter) }, finishedOneTurn)

	select {
	case in := <-got:
		if in.Text != "/etc/hosts 是什么" {
			t.Errorf("正文 = %q", in.Text)
		}
	default:
		t.Fatal("应发起一轮请求")
	}
	mustNotContain(t, plain(m), "已手动触发技能")
}

// 停用的技能不能手动触发，说明原因且不发起请求。
func TestTriggerDisabledSkillRefuses(t *testing.T) {
	r := fakeRunner{run: func(context.Context, agent.Input, event.Sink) error {
		t.Error("不该发起请求")
		return nil
	}}
	f := &fakeSkills{skills: skillList()}
	m := newSized(t, Options{Runner: r, Info: Info{Skills: f.Skills()}, Skills: f}, 120, 40)
	submit(m, "/pdf 提取文本")
	if m.running {
		t.Error("不该进入生成状态")
	}
	mustContain(t, plain(m), "已停用")
}

// 候选列表里列出启用的技能，排在内置命令之后。
func TestSkillSuggestions(t *testing.T) {
	f := &fakeSkills{skills: skillList()}
	m := newSized(t, Options{Runner: scriptRunner(), Info: Info{Skills: f.Skills()}, Skills: f}, 120, 40)
	m.in.SetValue("/la")
	m.syncSuggest()
	if len(m.sug.items) != 1 {
		t.Fatalf("候选 = %+v", m.sug.items)
	}
	if it := m.sug.items[0]; it.value != "/lark " || it.label != "/lark" {
		t.Errorf("候选 = %+v", it)
	}
	// 停用的技能不出现在候选里。
	m.in.SetValue("/pd")
	m.syncSuggest()
	if len(m.sug.items) != 0 {
		t.Errorf("停用技能不该出现: %+v", m.sug.items)
	}
}
