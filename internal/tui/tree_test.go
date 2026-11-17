package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// sampleTree：1 → 2 → 3，第 1 轮之后另开了 4 → 5，当前在 5。
//
//	1
//	├─ 4 → 5（当前路径，先列）
//	└─ 2 → 3
func sampleTree() *SessionTree {
	return &SessionTree{Current: 5, Nodes: []TreeNode{
		{Turn: 1, Parent: 0, Prompt: "问1", Reply: "答1"},
		{Turn: 2, Parent: 1, Prompt: "问2", Reply: "答2"},
		{Turn: 3, Parent: 2, Prompt: "问3", Reply: "答3"},
		{Turn: 4, Parent: 1, Prompt: "改问2", Reply: "答4"},
		{Turn: 5, Parent: 4, Prompt: "问5", Reply: "答5"},
	}}
}

func TestFlattenTree(t *testing.T) {
	var got []string
	for _, r := range flattenTree(sampleTree()) {
		mark := ""
		if r.onPath {
			mark = "*"
		}
		got = append(got, r.prefix+r.node.Prompt+mark)
	}
	want := []string{"问1*", "├─ 改问2*", "│  问5*", "└─ 问2", "   问3"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("摊平结果:\n%s\n期望:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// 会话开头就分了叉：顶层也画连接符，当前路径先列。
	root := &SessionTree{Current: 2, Nodes: []TreeNode{
		{Turn: 1, Parent: 0, Prompt: "甲"},
		{Turn: 2, Parent: 0, Prompt: "乙"},
	}}
	got = nil
	for _, r := range flattenTree(root) {
		got = append(got, r.prefix+r.node.Prompt)
	}
	if strings.Join(got, ",") != "├─ 乙,└─ 甲" {
		t.Fatalf("顶层分叉: %v", got)
	}
}

func openTree(t *testing.T, f *fakeSessions) *Model {
	t.Helper()
	m := sessionsModel(t, f)
	m.in.SetValue("/tree")
	_, cmd := m.Update(keyEnter)
	if m.treePick == nil || cmd == nil {
		t.Fatal("/tree 应打开面板并异步读取会话树")
	}
	runCmd(m, cmd)
	return m
}

func TestTreePanelNavigate(t *testing.T) {
	f := &fakeSessions{tree: sampleTree(), res: &Restored{Events: oldConversation(), Notice: "已切换到第 3 轮之后"}}
	m := openTree(t, f)
	s := plain(m)
	mustContain(t, s, "会话树", "改问2", "问3", "← 当前")
	if p := m.treePick; p.rows[p.cursor].node.Turn != 5 {
		t.Fatalf("光标应停在当前位置，得到第 %d 轮", p.rows[p.cursor].node.Turn)
	}

	// 在当前位置上按 Enter：什么都不做，直接关闭。
	_, cmd := m.Update(keyEnter)
	if cmd != nil || m.treePick != nil || len(f.navigated) != 0 {
		t.Fatal("选中当前位置时不应切换")
	}

	m = openTree(t, f)
	m.Update(press(tea.KeyEnd, 0)) // 最后一行是第 3 轮
	_, cmd = m.Update(keyEnter)
	if m.treePick != nil || !m.resuming || cmd == nil {
		t.Fatal("Enter 应关闭面板并开始切换")
	}
	runCmd(m, cmd)
	if len(f.navigated) != 1 || f.navigated[0] != 3 || m.resuming {
		t.Fatalf("Navigate 参数 = %v resuming=%v", f.navigated, m.resuming)
	}
	mustContain(t, plain(m), "上次的提问", "已切换到第 3 轮之后")
	if m.in.Value() != "" {
		t.Fatalf("Enter 切换不应改输入框: %q", m.in.Value())
	}
}

func TestTreePanelEditPrompt(t *testing.T) {
	f := &fakeSessions{tree: sampleTree(), res: &Restored{Events: oldConversation(), Notice: "已切换到第 1 轮之后"}}
	m := openTree(t, f)
	m.Update(press(tea.KeyUp, 0)) // 从第 5 轮上移到第 4 轮“改问2”
	_, cmd := m.Update(typed("e"))
	runCmd(m, cmd)
	if len(f.navigated) != 1 || f.navigated[0] != 1 {
		t.Fatalf("改问第 4 轮应切到它的上一轮（第 1 轮）: %v", f.navigated)
	}
	if m.in.Value() != "改问2" {
		t.Fatalf("原提问应放回输入框，得到 %q", m.in.Value())
	}

	// 切换失败时输入框保持原样，对话区不变。
	f2 := &fakeSessions{tree: sampleTree(), resumeErr: errors.New("会话文件被删了")}
	m2 := openTree(t, f2)
	begin(m2, "还在的内容")
	m2.running = false
	_, cmd = m2.Update(typed("e"))
	runCmd(m2, cmd)
	mustContain(t, plain(m2), "还在的内容", "切换分支失败：会话文件被删了")
	if m2.in.Value() != "" {
		t.Fatalf("失败时不应填入输入框: %q", m2.in.Value())
	}
}

func TestTreePanelEscAndErrors(t *testing.T) {
	f := &fakeSessions{tree: sampleTree()}
	m := openTree(t, f)
	m.Update(keyEsc)
	if m.treePick != nil || len(f.navigated) != 0 {
		t.Fatal("Esc 应关闭面板且不切换")
	}

	m = openTree(t, &fakeSessions{treeErr: errors.New("当前会话还没有任何提问")})
	if m.treePick != nil {
		t.Fatal("读取失败应关闭面板")
	}
	mustContain(t, plain(m), "没有可切换的分支：当前会话还没有任何提问")

	m = sessionsModel(t, &fakeSessions{tree: sampleTree()})
	begin(m, "进行中")
	m.in.SetValue("/tree")
	m.Update(keyEnter)
	if m.treePick != nil {
		t.Fatal("生成中不应打开会话树")
	}

	m = newSized(t, Options{Runner: scriptRunner()}, 100, 30)
	submit(m, "/tree")
	mustContain(t, plain(m), "当前不能查看会话树")
}
