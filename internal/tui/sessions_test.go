package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// 测试时钟（newSized 冻结在 2026-01-01 00:00 UTC）往前推。
var frozenNow = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func browserSessions() *fakeSessions {
	return &fakeSessions{
		list: []SessionInfo{
			{ID: "aaaa1111", Path: "p/a.jsonl", Prompt: "系统提示词的英文怎么说", Turns: 3, Updated: frozenNow.Add(-3 * time.Hour), Current: true, Cwd: `D:\proj`},
			{ID: "bbbb2222", Path: "p/b.jsonl", Prompt: "帮我搜索去年的国产旗舰手机", Turns: 50, Updated: frozenNow.Add(-4 * time.Hour), Cwd: `D:\proj`},
			{ID: "cccc3333", Path: "p/c.jsonl", Prompt: "把 markdown 上传到飞书", Name: "飞书上传", Turns: 44, Updated: frozenNow.Add(-26 * time.Hour), Cwd: `D:\proj`},
		},
		all: []SessionInfo{
			{ID: "dddd4444", Path: "q/d.jsonl", Prompt: "别的目录里的会话", Turns: 2, Updated: frozenNow.Add(-time.Minute), Cwd: `D:\other`},
		},
		res: &Restored{Notice: "已恢复"},
	}
}

// openBrowser 打开 /resume 并把异步加载的结果喂回来。
func openBrowser(t *testing.T, f *fakeSessions) *Model {
	t.Helper()
	m := sessionsModel(t, f)
	m.in.SetValue("/resume")
	_, cmd := m.Update(keyEnter)
	if m.browser == nil {
		t.Fatal("/resume 应打开会话浏览器")
	}
	runCmd(m, cmd)
	return m
}

func visibleIDs(m *Model) []string {
	var ids []string
	for _, it := range m.browser.visible() {
		ids = append(ids, it.ID)
	}
	return ids
}

func keyRune(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

func ctrl(r rune) tea.KeyPressMsg { return press(r, tea.ModCtrl) }

func TestBrowserListsSessions(t *testing.T) {
	m := openBrowser(t, browserSessions())
	screen := plain(m)
	mustContain(t, screen, "恢复会话（当前目录）", "◉ 当前目录", "○ 全部", "排序：最近", "Ctrl+R 重命名",
		"系统提示词的英文怎么说", "当前 · 3 轮  3小时前", "50 轮  4小时前", "飞书上传", "44 轮  1天前", "(1/3)")
	mustNotContain(t, screen, "把 markdown 上传到飞书") // 起过名字的显示名字
	if n := len(strings.Split(m.View().Content, "\n")); n != 40 {
		t.Fatalf("打开浏览器后画面 %d 行，应为 40", n)
	}
	if m.resuming {
		t.Error("只打开浏览器，不应开始恢复")
	}
}

func TestBrowserSearchModes(t *testing.T) {
	m := openBrowser(t, browserSessions())
	cases := []struct {
		q    string
		want []string
	}{
		{"手机", []string{"bbbb2222"}},
		{"飞书", []string{"cccc3333"}},          // 名字和提问都能搜到
		{"MARKDOWN 飞书", []string{"cccc3333"}}, // 多个关键词都要命中，不分大小写
		{"re:^系统|手机$", []string{"aaaa1111", "bbbb2222"}},
		{`"英文 怎么"`, nil}, // 精确短语要连着出现
		{`"英文怎么"`, []string{"aaaa1111"}},
		{"bbbb", []string{"bbbb2222"}}, // 按 ID 搜
	}
	for _, c := range cases {
		m.browser.query = c.q
		if got := visibleIDs(m); !slices.Equal(got, c.want) {
			t.Errorf("搜索 %q = %v，想要 %v", c.q, got, c.want)
		}
	}
	m.browser.query = "re:("
	mustContain(t, plain(m), "正则无效")

	// 键盘输入进搜索框，Backspace 删除，Ctrl+U 清空。
	m.browser.query = ""
	typeText(m, "手机")
	if m.browser.query != "手机" || len(visibleIDs(m)) != 1 {
		t.Fatalf("query=%q", m.browser.query)
	}
	m.Update(press(tea.KeyBackspace, 0))
	if m.browser.query != "手" {
		t.Errorf("Backspace 后 = %q", m.browser.query)
	}
	m.Update(ctrl('u'))
	if m.browser.query != "" {
		t.Errorf("Ctrl+U 后 = %q", m.browser.query)
	}
	m.Update(tea.PasteMsg{Content: "飞书\n上传"})
	if m.browser.query != "飞书 上传" || m.in.Value() != "" {
		t.Errorf("粘贴应进搜索框：query=%q input=%q", m.browser.query, m.in.Value())
	}
}

func TestBrowserScopeSortAndNamed(t *testing.T) {
	f := browserSessions()
	m := openBrowser(t, f)

	_, cmd := m.Update(keyTab)
	runCmd(m, cmd)
	if !slices.Equal(f.listAll, []bool{false, true}) || !slices.Equal(visibleIDs(m), []string{"dddd4444"}) {
		t.Fatalf("Tab 应改为列出全部目录：listAll=%v ids=%v", f.listAll, visibleIDs(m))
	}
	mustContain(t, plain(m), "恢复会话（全部目录）")
	m.Update(ctrl('p'))
	mustContain(t, plain(m), `D:\other`, "路径（开）")
	_, cmd = m.Update(keyTab)
	runCmd(m, cmd)

	m.Update(ctrl('s')) // 最早
	if got := visibleIDs(m); !slices.Equal(got, []string{"cccc3333", "bbbb2222", "aaaa1111"}) {
		t.Errorf("最早排序 = %v", got)
	}
	m.Update(ctrl('s')) // 轮数
	if got := visibleIDs(m); !slices.Equal(got, []string{"bbbb2222", "cccc3333", "aaaa1111"}) {
		t.Errorf("轮数排序 = %v", got)
	}
	mustContain(t, plain(m), "排序：轮数")
	m.Update(ctrl('s'))

	m.Update(ctrl('n'))
	if got := visibleIDs(m); !slices.Equal(got, []string{"cccc3333"}) {
		t.Errorf("只看已命名 = %v", got)
	}
	mustContain(t, plain(m), "名称：已命名")
}

func TestBrowserEnterResumesSelected(t *testing.T) {
	f := browserSessions()
	m := openBrowser(t, f)
	m.Update(press(tea.KeyDown, 0))
	_, cmd := m.Update(keyEnter)
	if m.browser != nil || !m.resuming {
		t.Fatal("回车应关闭浏览器并开始恢复")
	}
	runCmd(m, cmd)
	if f.gotSel != "p/b.jsonl" {
		t.Fatalf("应按路径恢复选中的会话，得到 %q", f.gotSel)
	}
	mustContain(t, plain(m), "已恢复")
}

func TestBrowserEnterOnCurrentSessionDoesNothing(t *testing.T) {
	f := browserSessions()
	m := openBrowser(t, f)
	m.Update(keyEnter)
	if m.browser != nil || m.resuming || f.gotSel != "" {
		t.Fatal("选中当前会话回车只关闭浏览器")
	}
	mustContain(t, plain(m), "已经在这个会话里了")
}

func TestBrowserRename(t *testing.T) {
	f := browserSessions()
	m := openBrowser(t, f)
	m.Update(press(tea.KeyDown, 0))
	m.Update(ctrl('r'))
	mustContain(t, plain(m), "重命名")
	typeText(m, "手机对比")
	_, cmd := m.Update(keyEnter) // 执行重命名
	runCmd(m, cmd)               // 结果
	runCmd(m, m.loadSessions())  // 重新加载（真实程序里由结果消息触发）
	if f.renamed["p/b.jsonl"] != "手机对比" {
		t.Fatalf("renamed = %v", f.renamed)
	}
	if sel, _ := m.browser.selected(); sel.Path != "p/b.jsonl" || sel.Name != "手机对比" {
		t.Errorf("重命名后应停在同一个会话上: %+v", sel)
	}
	mustContain(t, plain(m), "手机对比")

	// 清空名字等于清除；Esc 取消不改动。
	m.Update(ctrl('r'))
	m.Update(press(tea.KeyEscape, 0))
	if m.browser == nil || m.browser.mode != modeBrowse || len(f.renamed) != 1 {
		t.Fatal("Esc 只取消重命名，不关闭浏览器")
	}
	mustContain(t, plain(m), "已取消重命名")
}

func TestBrowserDeleteNeedsConfirmation(t *testing.T) {
	f := browserSessions()
	m := openBrowser(t, f)

	m.Update(ctrl('d')) // 当前会话不能删
	if m.browser.mode != modeBrowse {
		t.Fatal("当前会话不应进入删除确认")
	}
	mustContain(t, plain(m), "不能删除")

	m.Update(press(tea.KeyDown, 0))
	m.Update(ctrl('d'))
	mustContain(t, plain(m), "删除「帮我搜索去年的国产旗舰手机」？", "按 y 确认")
	m.Update(keyRune('n'))
	if len(f.deleted) != 0 {
		t.Fatal("没按 y 不应删除")
	}
	mustContain(t, plain(m), "已取消删除")

	m.Update(ctrl('d'))
	_, cmd := m.Update(keyRune('y'))
	runCmd(m, cmd)
	if !slices.Equal(f.deleted, []string{"p/b.jsonl"}) {
		t.Fatalf("deleted = %v", f.deleted)
	}
	mustContain(t, plain(m), "已删除「帮我搜索去年的国产旗舰手机」")
}

func TestBrowserOperationErrorsShown(t *testing.T) {
	f := browserSessions()
	f.opErr = errors.New("磁盘只读")
	m := openBrowser(t, f)
	m.Update(press(tea.KeyDown, 0))
	m.Update(ctrl('d'))
	_, cmd := m.Update(keyRune('y'))
	runCmd(m, cmd)
	mustContain(t, plain(m), "删除失败：磁盘只读")
}

func TestBrowserEmptyErrorAndEscape(t *testing.T) {
	m := openBrowser(t, &fakeSessions{})
	mustContain(t, plain(m), "当前目录下还没有会话，按 Tab 查看全部目录")

	m = openBrowser(t, &fakeSessions{listErr: errors.New("磁盘错误")})
	mustContain(t, plain(m), "读取会话列表失败：磁盘错误")
	m.Update(press(tea.KeyEscape, 0))
	if m.browser != nil {
		t.Fatal("Esc 应关闭浏览器")
	}
	mustContain(t, plain(m), "已取消恢复会话")
}

// 切换范围后，旧范围的结果晚到时要丢掉。
func TestBrowserDropsStaleLoads(t *testing.T) {
	f := browserSessions()
	m := sessionsModel(t, f)
	m.in.SetValue("/resume")
	_, first := m.Update(keyEnter)
	_, second := m.Update(keyTab)
	runCmd(m, second)
	runCmd(m, first) // 过期的“当前目录”结果
	if !slices.Equal(visibleIDs(m), []string{"dddd4444"}) {
		t.Fatalf("过期结果覆盖了新结果: %v", visibleIDs(m))
	}
}

// 列表长于可见行数时跟着光标滚动，并显示位置。
func TestBrowserScrollsWithCursor(t *testing.T) {
	f := &fakeSessions{}
	for i := range 30 {
		f.list = append(f.list, SessionInfo{ID: string(rune('a'+i%26)) + "x", Path: "p" + string(rune('A'+i)), Prompt: "会话" + string(rune('A'+i)),
			Turns: 1, Updated: frozenNow.Add(-time.Duration(i) * time.Minute)})
	}
	m := openBrowser(t, f)
	for range 20 {
		m.Update(press(tea.KeyDown, 0))
	}
	screen := plain(m)
	mustContain(t, screen, "会话U", "(21/30)")
	mustNotContain(t, screen, "会话A")
	m.Update(press(tea.KeyHome, 0))
	mustContain(t, plain(m), "会话A", "(1/30)")
}

func TestAgo(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Second: "刚刚", 5 * time.Minute: "5分钟前", 3 * time.Hour: "3小时前",
		50 * time.Hour: "2天前", 40 * 24 * time.Hour: "2025-11-22",
	} {
		if got := ago(frozenNow, frozenNow.Add(-d)); got != want {
			t.Errorf("ago(%v) = %q，想要 %q", d, got, want)
		}
	}
}
