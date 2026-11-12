package tui

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func typeText(m *Model, s string) {
	for _, r := range s {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

var keyTab = press(tea.KeyTab, 0)

func sugLabels(m *Model) []string {
	var out []string
	for _, it := range m.sug.items {
		out = append(out, it.label)
	}
	return out
}

// 输入 /eff 就出现候选，Tab 补全成带空格的完整命令，接着列出档位。
func TestSuggestCommandAndTabComplete(t *testing.T) {
	m := effortModel(t, testEfforts())
	typeText(m, "/eff")
	if got := sugLabels(m); !slices.Equal(got, []string{"/effort"}) {
		t.Fatalf("候选 = %v", got)
	}
	mustContain(t, plain(m), "/effort [档位]", "调整思考程度", "Tab 补全")

	m.Update(keyTab)
	if m.in.Value() != "/effort " {
		t.Fatalf("Tab 后输入框 = %q", m.in.Value())
	}
	if got := sugLabels(m); !slices.Equal(got, []string{"auto", "low", "high", "max"}) {
		t.Fatalf("参数候选 = %v", got)
	}
	mustContain(t, plain(m), "reasoning_effort=high", "← 当前")

	typeText(m, "m")
	m.Update(keyTab)
	if m.in.Value() != "/effort max" {
		t.Fatalf("参数补全 = %q", m.in.Value())
	}
}

// 屏幕高度不变：候选列表占的行从对话区里扣，不把底部挤出屏幕。
func TestSuggestKeepsScreenHeight(t *testing.T) {
	m := effortModel(t, testEfforts())
	typeText(m, "/")
	if len(m.sug.items) != len(slashCommands) {
		t.Fatalf("只输入 / 应列出全部命令，得到 %d 个", len(m.sug.items))
	}
	if n := len(strings.Split(m.View().Content, "\n")); n != 30 {
		t.Fatalf("画面 %d 行，应为 30", n)
	}
	mustContain(t, plain(m), "(1/") // 超过可见行数时显示位置
}

// 回车：只输入了一部分就执行选中项；输入已经完整就照原样执行，不被别的候选顶掉。
func TestSuggestEnterSemantics(t *testing.T) {
	e := testEfforts()
	m := effortModel(t, e)
	typeText(m, "/eff")
	m.Update(keyEnter)
	if m.effort == nil {
		t.Fatal("/eff 回车应执行 /effort，打开档位条")
	}
	m.Update(press(tea.KeyEscape, 0))

	typeText(m, "/effort ")
	m.Update(keyEnter)
	if m.effort == nil || len(e.sets) != 0 {
		t.Fatal("只敲了 \"/effort \" 回车应打开档位条，而不是选第一个档位")
	}
	m.Update(press(tea.KeyEscape, 0))

	typeText(m, "/effort lo")
	m.Update(keyEnter)
	if len(e.sets) != 1 || e.sets[0] != "low" {
		t.Fatalf("/effort lo 回车应切到 low，得到 %v", e.sets)
	}

	// /exit 与 /quit 都完整时各自执行；/ex 补成 /exit。
	m.in.SetValue("")
	typeText(m, "/ex")
	if _, cmd := m.Update(keyEnter); !isQuit(cmd) {
		t.Fatal("/ex 回车应执行 /exit")
	}
}

func TestSuggestArrowsWrapAndPick(t *testing.T) {
	e := testEfforts()
	m := effortModel(t, e)
	typeText(m, "/effort ")
	m.Update(press(tea.KeyUp, 0)) // 从第一项往上绕到最后一项
	if m.sug.sel != 3 {
		t.Fatalf("sel = %d", m.sug.sel)
	}
	m.Update(keyEnter) // 用 ↑↓ 选过就执行选中项
	if len(e.sets) != 1 || e.sets[0] != "max" {
		t.Fatalf("应切到 max，得到 %v", e.sets)
	}
}

// Esc 关闭候选，同样的输入不再弹出；继续打字又会出现。
func TestSuggestEscapeDismisses(t *testing.T) {
	m := effortModel(t, testEfforts())
	typeText(m, "/he")
	m.Update(press(tea.KeyEscape, 0))
	if len(m.sug.items) != 0 || m.in.Value() != "/he" {
		t.Fatalf("Esc 后候选 = %v，输入 = %q", sugLabels(m), m.in.Value())
	}
	m.Update(keyEnter) // 关闭后回车按原样提交：/he 不是命令，发给模型
	if !m.running {
		t.Fatal("关闭候选后回车应原样提交")
	}
}

// 不是命令的输入没有候选：路径、未知命令、多行、命令后面的普通参数。
func TestSuggestIgnoresNonCommands(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner(), Models: testModels()}, 100, 30)
	for _, v := range []string{"/etc/hosts 是什么", "/zzz", "hello /effort", "/image D:\\a.png", "/help\nmore"} {
		m.in.SetValue(v)
		m.syncSuggest()
		if len(m.sug.items) != 0 {
			t.Errorf("%q 不应有候选，得到 %v", v, sugLabels(m))
		}
	}
	// Tab 在没有候选时交给输入框，不报错也不清空。
	m.in.SetValue("普通文字")
	m.Update(keyTab)
	if m.in.Value() != "普通文字" {
		t.Errorf("Tab 改了普通输入: %q", m.in.Value())
	}
}

// /model 的参数按包含匹配，长引用只输一段就能找到。
func TestSuggestModelArgument(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner(), Models: testModels()}, 100, 30)
	typeText(m, "/model thr")
	if got := sugLabels(m); !slices.Equal(got, []string{"c/three"}) {
		t.Fatalf("候选 = %v", got)
	}
	m.Update(keyTab)
	if m.in.Value() != "/model c/three" {
		t.Fatalf("补全 = %q", m.in.Value())
	}
}

// 选择器或档位条打开时不显示候选。
func TestSuggestHiddenUnderOverlays(t *testing.T) {
	m := effortModel(t, testEfforts())
	submit(m, "/effort")
	m.syncSuggest()
	if len(m.sug.items) != 0 {
		t.Fatal("档位条打开时不应有候选")
	}
}

// slash() 里处理的命令都要登记进 slashCommands，否则补全里看不到；反之亦然。
func TestSlashCommandsMatchDispatcher(t *testing.T) {
	src, err := os.ReadFile("commands.go")
	if err != nil {
		t.Fatal(err)
	}
	// Windows 上检出为 CRLF，下面的 "\n}\n" 就匹配不到，会切到文件末尾并混进
	// 其他函数里的 case。先统一成 LF 再解析。
	body := strings.ReplaceAll(string(src), "\r\n", "\n")
	start := strings.Index(body, "func (m *Model) slash(")
	if start < 0 {
		t.Fatal("找不到 slash()，测试要跟着实现改")
	}
	body = body[start:]
	end := strings.Index(body, "\n}\n")
	if end < 0 {
		t.Fatal("找不到 slash() 的结尾")
	}
	body = body[:end]
	var handled []string
	for _, c := range regexp.MustCompile(`case ([^:]+):`).FindAllStringSubmatch(body, -1) {
		for _, q := range regexp.MustCompile(`"(/[^"]+)"`).FindAllStringSubmatch(c[1], -1) {
			handled = append(handled, q[1])
		}
	}
	var listed []string
	for _, c := range slashCommands {
		listed = append(listed, c.name)
	}
	slices.Sort(handled)
	slices.Sort(listed)
	if !slices.Equal(handled, listed) {
		t.Errorf("slash() 处理 %v\nslashCommands 登记 %v", handled, listed)
	}
}
