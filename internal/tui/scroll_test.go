package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shumengya/sprout/internal/agent/event"
)

func wheel(b tea.MouseButton) tea.MouseWheelMsg { return tea.MouseWheelMsg{Button: b, X: 5, Y: 5} }

func keyOf(code rune, mod tea.KeyMod) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code, Mod: mod} }

// scrolled 返回一个已填满内容、停在底部的 Model。
func scrolled(t *testing.T) *Model {
	t.Helper()
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 24)
	fillTranscript(m, 100)
	if !m.follow || !m.vp.AtBottom() {
		t.Fatal("填充后应停在底部")
	}
	return m
}

func TestWheelUpScrollsAndPausesFollow(t *testing.T) {
	m := scrolled(t)
	bottom := m.vp.YOffset()
	m.Update(wheel(tea.MouseWheelUp))
	if m.follow || m.vp.YOffset() != bottom-wheelStep {
		t.Fatalf("滚轮上滚一格应上移 %d 行并暂停跟随: follow=%v offset %d→%d", wheelStep, m.follow, bottom, m.vp.YOffset())
	}
	m.Update(wheel(tea.MouseWheelDown))
	if !m.follow || m.vp.YOffset() != bottom {
		t.Fatalf("滚回底部应恢复跟随: follow=%v offset=%d", m.follow, m.vp.YOffset())
	}
}

func TestWheelDownAtBottomIsHarmless(t *testing.T) {
	m := scrolled(t)
	before := m.vp.YOffset()
	m.Update(wheel(tea.MouseWheelDown))
	if m.vp.YOffset() != before || !m.follow {
		t.Fatal("已在底部时继续下滚不应有变化")
	}
}

func TestOtherMouseEventsAreIgnored(t *testing.T) {
	m := scrolled(t)
	m.in.SetValue("草稿")
	before := m.vp.YOffset()
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 3, Y: 3})
	m.Update(tea.MouseMotionMsg{X: 4, Y: 4})
	if m.vp.YOffset() != before || m.in.Value() != "草稿" || !m.follow {
		t.Fatal("点击和拖动不应影响滚动位置或输入框")
	}
}

func TestScrollKeysWithEmptyInput(t *testing.T) {
	m := scrolled(t)
	bottom := m.vp.YOffset()

	m.Update(keyOf(tea.KeyUp, tea.ModCtrl))
	if m.vp.YOffset() != bottom-1 || m.follow {
		t.Fatalf("Ctrl+↑ 应上滚一行: offset=%d", m.vp.YOffset())
	}
	m.Update(keyOf(tea.KeyDown, tea.ModCtrl))
	if m.vp.YOffset() != bottom || !m.follow {
		t.Fatalf("Ctrl+↓ 应回到底部: offset=%d", m.vp.YOffset())
	}
	m.Update(keyOf(tea.KeyHome, tea.ModCtrl))
	if m.vp.YOffset() != 0 || m.follow {
		t.Fatalf("Ctrl+Home 应跳到最前: offset=%d", m.vp.YOffset())
	}
	m.Update(keyOf(tea.KeyEnd, tea.ModCtrl))
	if m.vp.YOffset() != bottom || !m.follow {
		t.Fatalf("Ctrl+End 应跳到最后: offset=%d", m.vp.YOffset())
	}
}

func TestPlainArrowsScrollOnlyWhenInputEmpty(t *testing.T) {
	m := scrolled(t)
	bottom := m.vp.YOffset()

	m.Update(keyOf(tea.KeyUp, 0))
	if m.vp.YOffset() != bottom-1 {
		t.Fatalf("输入框为空时 ↑ 应滚动对话: offset=%d", m.vp.YOffset())
	}
	m.Update(keyOf(tea.KeyEnd, 0))
	if m.vp.YOffset() != bottom {
		t.Fatalf("输入框为空时 End 应到底部: offset=%d", m.vp.YOffset())
	}

	m.in.SetValue("第一行\n第二行")
	m.fit() // 两行输入让输入框长高一行；跟随底部时对话区仍贴底
	bottom = m.vp.YOffset()
	m.Update(keyOf(tea.KeyUp, 0))
	if m.vp.YOffset() != bottom || !m.follow {
		t.Fatal("输入框有内容时 ↑ 应留给输入框移动光标，不能滚动对话")
	}
}

func TestPageKeysStillWorkWithText(t *testing.T) {
	m := scrolled(t)
	m.in.SetValue("草稿")
	m.Update(keyPgUp)
	if m.follow {
		t.Fatal("输入框有内容时 PgUp 也应滚动对话")
	}
}

func TestScrollHintOverlaysLastRowWithoutResizing(t *testing.T) {
	// 上滚提示叠在对话区最后一行，不占新行：否则对话区变矮，滚回去会差一行到不了底。
	m := scrolled(t)
	h := m.vp.Height()
	m.Update(keyPgUp)
	if m.vp.Height() != h {
		t.Fatalf("上滚后对话区高度从 %d 变成 %d", h, m.vp.Height())
	}
	lines := strings.Split(plain(m), "\n")
	if row := lines[h-1]; !strings.HasSuffix(strings.TrimRight(row, " "), "End 回到底部") {
		t.Fatalf("提示应在对话区最后一行右端: %q", row)
	}
	for i, l := range strings.Split(m.View().Content, "\n") {
		if w := ansi.StringWidth(l); w > 80 {
			t.Errorf("第 %d 行宽 %d 超出终端", i, w)
		}
	}
	if len(lines) != 24 {
		t.Fatalf("画面 %d 行", len(lines))
	}
}

// 活动行（比如“已复制”）和历史提示同时在时，两者应显示在同一行，不能一上一下错开。
// 先上滚再复制：复制的画面来自鼠标松开，不会清掉正在显示的上滚提示。
func TestScrollHintJoinsActivityLine(t *testing.T) {
	m := scrolled(t)
	m.Update(keyPgUp)
	m.onCopied(copiedMsg{n: 37})

	found := ""
	for _, l := range strings.Split(plain(m), "\n") {
		if strings.Contains(l, "已复制 37 个字符") {
			found = l
		}
	}
	if found == "" {
		t.Fatal("活动行应显示复制提示")
	}
	if !strings.HasSuffix(strings.TrimRight(found, " "), "End 回到底部") {
		t.Fatalf("历史提示应与复制提示同一行: %q", found)
	}
	if len(strings.Split(plain(m), "\n")) != 25 {
		t.Error("活动行本来就占一行，历史提示叠上去不该再加一行")
	}
}

func TestStatusShowsHistoryPositionAndNewContent(t *testing.T) {
	m := scrolled(t)
	mustNotContain(t, plain(m), "历史", "有新内容")

	m.Update(keyPgUp)
	s := plain(m)
	mustContain(t, s, "历史", "End 回到底部")
	mustNotContain(t, s, "有新内容")

	begin(m, "q") // 上滚期间新内容到达
	m.follow = false
	apply(m, event.MessageDelta{Text: "后来的回答"})
	mustContain(t, plain(m), "有新内容")

	m.Update(keyOf(tea.KeyEnd, 0))
	mustNotContain(t, plain(m), "历史", "有新内容")
	mustContain(t, plain(m), "后来的回答")
}

func TestResizeDoesNotFakeNewContentNotice(t *testing.T) {
	m := scrolled(t)
	m.Update(keyPgUp)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	mustNotContain(t, plain(m), "有新内容")
}

func TestMouseModeFollowsToggle(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 24)
	if m.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal("默认应开启鼠标上报，滚轮才能工作")
	}
	submit(m, "/mouse")
	if m.View().MouseMode != tea.MouseModeNone {
		t.Fatal("/mouse 后应关闭鼠标上报")
	}
	mustContain(t, plain(m), "鼠标滚轮已关闭", "直接用鼠标选中文字")
	submit(m, "/mouse")
	if m.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal("再次 /mouse 应重新开启")
	}
	mustContain(t, plain(m), "按住 Shift")
}

func TestHelpDocumentsScrolling(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 60)
	submit(m, "/help")
	mustContain(t, plain(m), "鼠标滚轮", "PgUp PgDn", "Ctrl+Home", "/think", "/mouse")
}

func TestWelcomeHintMentionsScrolling(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 24)
	mustContain(t, plain(m), "PgUp / PgDn", "鼠标滚轮", "/help")
}
