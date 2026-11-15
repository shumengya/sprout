package tui

import (
	"errors"
	"testing"
)

// fakeUpdater 是 Updater 的替身，只在 _test.go 里。
type fakeUpdater struct {
	notice  string
	report  string
	err     error
	updates int
}

func (f *fakeUpdater) Check() string           { return f.notice }
func (f *fakeUpdater) Update() (string, error) { f.updates++; return f.report, f.err }

func TestUpdateCommandRunsInCmd(t *testing.T) {
	fu := &fakeUpdater{report: "已更新到 2026.11.20，重新启动 sprout 后生效。"}
	m := newSized(t, Options{Runner: scriptRunner(), Updater: fu}, 100, 30)
	m.in.SetValue("/update")
	_, cmd := m.Update(keyEnter)
	if cmd == nil || fu.updates != 0 {
		t.Fatalf("更新要放在 tea.Cmd 里做: cmd=%v updates=%d", cmd != nil, fu.updates)
	}
	mustContain(t, plain(m), "正在检查并下载更新")
	m.Update(cmd())
	mustContain(t, plain(m), "已更新到 2026.11.20")
	mustNotContain(t, plain(m), "正在检查并下载更新")
}

func TestUpdateCommandShowsError(t *testing.T) {
	fu := &fakeUpdater{err: errors.New("校验失败")}
	m := newSized(t, Options{Runner: scriptRunner(), Updater: fu}, 100, 30)
	runSlash(t, m, "/update")
	mustContain(t, plain(m), "更新失败：校验失败")
}

func TestUpdateCommandWithoutUpdater(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 30)
	runSlash(t, m, "/update")
	mustContain(t, plain(m), "不支持自动更新")
}

func TestStartupUpdateNotice(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner(), Updater: &fakeUpdater{notice: "有新版本可用：2026.11.20"}}, 100, 30)
	m.Update(m.updateCheckCmd()())
	mustContain(t, plain(m), "有新版本可用：2026.11.20")

	// 没有新版本时什么都不显示。
	quiet := newSized(t, Options{Runner: scriptRunner(), Updater: &fakeUpdater{}}, 100, 30)
	before := plain(quiet)
	quiet.Update(quiet.updateCheckCmd()())
	if plain(quiet) != before {
		t.Fatal("没有新版本时不该添加提示")
	}
	if (&Model{}).updateCheckCmd() != nil {
		t.Fatal("没有 Updater 时不该发起检查")
	}
}
