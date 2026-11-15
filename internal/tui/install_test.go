package tui

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// fakeInstaller 是 Installer 的替身，记录调用次数。只在 _test.go 里。
type fakeInstaller struct {
	installs, uninstalls int
	report               string
	err                  error
}

func (f *fakeInstaller) Install() (string, error)   { f.installs++; return f.report, f.err }
func (f *fakeInstaller) Uninstall() (string, error) { f.uninstalls++; return f.report, f.err }

// runSlash 提交一条斜杠命令并执行它返回的命令，把结果喂回 Update。
func runSlash(t *testing.T, m *Model, text string) {
	t.Helper()
	m.in.SetValue(text)
	_, cmd := m.Update(keyEnter)
	if cmd != nil {
		m.Update(cmd())
	}
}

func TestInstallCommandRunsInCmdAndShowsReport(t *testing.T) {
	fi := &fakeInstaller{report: "已注册全局 sprout 命令，指向 C:\\x\\sprout.exe"}
	m := newSized(t, Options{Runner: scriptRunner(), Installer: fi}, 100, 30)

	m.in.SetValue("/install-sprout")
	_, cmd := m.Update(keyEnter)
	if cmd == nil || fi.installs != 0 {
		t.Fatalf("注册要放在 tea.Cmd 里做，Update 里不能直接调用: cmd=%v installs=%d", cmd != nil, fi.installs)
	}
	mustContain(t, plain(m), "正在注册全局 sprout 命令")
	m.Update(cmd())
	if fi.installs != 1 {
		t.Fatalf("installs = %d", fi.installs)
	}
	mustContain(t, plain(m), "已注册全局 sprout 命令")
	mustNotContain(t, plain(m), "正在注册")
}

func TestUninstallCommand(t *testing.T) {
	fi := &fakeInstaller{report: "已卸载全局 sprout 命令"}
	m := newSized(t, Options{Runner: scriptRunner(), Installer: fi}, 100, 30)
	runSlash(t, m, "/uninstall-sprout")
	if fi.uninstalls != 1 || fi.installs != 0 {
		t.Fatalf("installs=%d uninstalls=%d", fi.installs, fi.uninstalls)
	}
	mustContain(t, plain(m), "已卸载全局 sprout 命令")
}

func TestInstallErrorIsShown(t *testing.T) {
	fi := &fakeInstaller{err: errors.New("无法写入用户 PATH: 拒绝访问")}
	m := newSized(t, Options{Runner: scriptRunner(), Installer: fi}, 100, 30)
	runSlash(t, m, "/install-sprout")
	mustContain(t, plain(m), "✗ 错误：无法写入用户 PATH: 拒绝访问")
}

func TestInstallWithoutInstaller(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 30)
	m.in.SetValue("/install-sprout")
	if _, cmd := m.Update(keyEnter); cmd != nil {
		t.Fatal("没有 Installer 时不应产生命令")
	}
	mustContain(t, plain(m), "不支持注册全局命令")
	if m.running {
		t.Fatal("本地命令不应启动 agent")
	}
}

func TestInstallCommandsInHelp(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 60)
	submit(m, "/help")
	mustContain(t, plain(m), "/install-sprout", "/uninstall-sprout")
}

var _ tea.Msg = installDoneMsg{}
