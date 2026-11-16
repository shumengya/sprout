package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func askFor(fields []AskField) (*Ask, <-chan AskAnswer) {
	reply := make(chan AskAnswer, 1)
	return &Ask{Server: "fs", Title: "部署", Message: "请填写参数", Fields: fields, Reply: reply}, reply
}

func openAsk(t *testing.T, fields []AskField) (*Model, <-chan AskAnswer) {
	t.Helper()
	asks := make(chan *Ask, 2)
	m := newSized(t, Options{Asks: asks}, 80, 30)
	a, reply := askFor(fields)
	asks <- a
	m.Update(askMsg{a})
	return m, reply
}

func answered(t *testing.T, reply <-chan AskAnswer) AskAnswer {
	t.Helper()
	select {
	case ans := <-reply:
		return ans
	case <-time.After(time.Second):
		t.Fatal("没有收到回答")
	}
	return AskAnswer{}
}

func TestAskFormSubmit(t *testing.T) {
	m, reply := openAsk(t, []AskField{
		{Name: "env", Title: "环境", Kind: "string", Required: true},
		{Name: "count", Title: "数量", Kind: "integer", Default: 3},
		{Name: "confirm", Title: "确认", Kind: "boolean"},
		{Name: "level", Title: "级别", Kind: "enum", Choices: []string{"dev", "prod"}, Labels: []string{"开发", "生产"}},
	})
	if !strings.Contains(plain(m), "MCP 服务端 fs 请求输入") || !strings.Contains(plain(m), "环境（必填）") {
		t.Fatalf("表单未显示：\n%s", plain(m))
	}

	m.Update(typed("prod"))
	m.Update(press(tea.KeyDown, 0))
	m.Update(press(tea.KeyDown, 0))
	m.Update(press(tea.KeyRight, 0)) // 确认：否 → 是
	m.Update(press(tea.KeyDown, 0))
	m.Update(press(tea.KeyRight, 0)) // 级别：未选 → 开发
	m.Update(press(tea.KeyDown, 0))  // 落到提交
	m.Update(keyEnter)

	ans := answered(t, reply)
	if ans.Action != AskAccept || ans.Values["env"] != "prod" || ans.Values["count"] != int64(3) ||
		ans.Values["confirm"] != true || ans.Values["level"] != "dev" {
		t.Fatalf("回答 = %+v", ans)
	}
	if m.ask != nil {
		t.Error("提交后表单应关闭")
	}
}

// 必填没填时停在表单上，不回半截答案。
func TestAskRequiredStaysOpen(t *testing.T) {
	m, reply := openAsk(t, []AskField{{Name: "env", Kind: "string", Required: true}})
	m.Update(press(tea.KeyDown, 0))
	m.Update(keyEnter)
	select {
	case ans := <-reply:
		t.Fatalf("不该回答：%+v", ans)
	default:
	}
	if m.ask == nil || !strings.Contains(plain(m), "必填") {
		t.Fatalf("应提示必填：\n%s", plain(m))
	}

	m.Update(press(tea.KeyUp, 0))
	m.Update(typed("12"))
	m.Update(press(tea.KeyDown, 0))
	m.Update(keyEnter)
	if ans := answered(t, reply); ans.Values["env"] != "12" {
		t.Fatalf("补填后应提交，得到 %+v", ans)
	}
}

func TestAskTypeValidation(t *testing.T) {
	m, reply := openAsk(t, []AskField{
		{Name: "n", Title: "次数", Kind: "integer", Required: true},
		{Name: "rate", Title: "比例", Kind: "number"},
	})
	m.Update(typed("3.5"))
	m.Update(press(tea.KeyDown, 0))
	m.Update(typed("x"))
	m.Update(press(tea.KeyDown, 0))
	m.Update(keyEnter)
	if !strings.Contains(plain(m), "次数：需要整数") {
		t.Fatalf("应指出整数错误：\n%s", plain(m))
	}
	m.Update(press(tea.KeyBackspace, 0))
	m.Update(press(tea.KeyBackspace, 0))
	m.Update(press(tea.KeyBackspace, 0))
	m.Update(typed("4"))
	m.Update(press(tea.KeyDown, 0))
	m.Update(press(tea.KeyBackspace, 0))
	m.Update(typed("0.5"))
	m.Update(press(tea.KeyDown, 0))
	m.Update(keyEnter)
	ans := answered(t, reply)
	if ans.Values["n"] != int64(4) || ans.Values["rate"] != 0.5 {
		t.Fatalf("回答 = %+v", ans)
	}
}

func TestAskDeclineAndCancel(t *testing.T) {
	m, reply := openAsk(t, nil)
	if !strings.Contains(plain(m), "允许") {
		t.Fatalf("纯确认应显示允许：\n%s", plain(m))
	}
	m.Update(keyCtrlC)
	if ans := answered(t, reply); ans.Action != AskDecline {
		t.Fatalf("Ctrl+C 应拒绝，得到 %+v", ans)
	}

	m, reply = openAsk(t, nil)
	m.Update(keyEsc)
	if ans := answered(t, reply); ans.Action != AskCancel {
		t.Fatalf("Esc 应取消，得到 %+v", ans)
	}
}

// 同时来了两份时先答第一份，答完自动接着问第二份。
func TestAskQueues(t *testing.T) {
	m, reply1 := openAsk(t, nil)
	a2, reply2 := askFor([]AskField{{Name: "x", Kind: "string"}})
	m.Update(askMsg{a2})
	if !strings.Contains(plain(m), "请填写参数") || strings.Contains(plain(m), "x") {
		t.Fatalf("应先显示第一份：\n%s", plain(m))
	}
	m.Update(keyEnter)
	if ans := answered(t, reply1); ans.Action != AskAccept {
		t.Fatalf("第一份 = %+v", ans)
	}
	if m.ask == nil || m.ask.ask != a2 {
		t.Fatal("答完应接着显示第二份")
	}
	m.Update(keyEsc)
	if ans := answered(t, reply2); ans.Action != AskCancel {
		t.Fatalf("第二份 = %+v", ans)
	}
}

// 退出时未答的请求要全部取消，不能让 MCP 调用一直挂着。
func TestAskCancelledOnShutdown(t *testing.T) {
	m, reply := openAsk(t, nil)
	a2, reply2 := askFor(nil)
	m.Update(askMsg{a2})
	m.Shutdown(time.Second)
	if ans := answered(t, reply); ans.Action != AskCancel {
		t.Fatalf("正在作答的应取消，得到 %+v", ans)
	}
	if ans := answered(t, reply2); ans.Action != AskCancel {
		t.Fatalf("排队的应取消，得到 %+v", ans)
	}
}

// 表单打开时按键不能漏到对话：Esc 只取消表单，不取消正在生成的一轮。
func TestAskConsumesKeys(t *testing.T) {
	m, reply := openAsk(t, nil)
	begin(m, "hi")
	m.Update(keyEsc)
	if !m.running {
		t.Error("Esc 应被表单消费，不该取消生成")
	}
	if ans := answered(t, reply); ans.Action != AskCancel {
		t.Fatalf("得到 %+v", ans)
	}
}
