package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shumengya/sprout/internal/agent/event"
)

func submit(m *Model, text string) {
	m.in.SetValue(text)
	m.Update(keyEnter)
}

func TestSlashHelp(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 70)
	submit(m, "/help")
	mustContain(t, plain(m), "快捷键", "Ctrl+J", "/tools", "PgUp")
	if m.running || m.in.Value() != "" {
		t.Fatal("本地命令不应启动 agent，且应清空输入框")
	}
}

func TestSlashToolsReport(t *testing.T) {
	info := testInfo
	info.Unavailable = []string{"bash 不可用：未找到 Git Bash"}
	m := newSized(t, Options{Runner: scriptRunner(), Info: info}, 120, 40)
	submit(m, "/tools")
	mustContain(t, plain(m), "可用终端工具:", "powershell  PowerShell 7+", "bash  Git Bash", "bash 不可用：未找到 Git Bash")
}

func TestSlashToolsWithNoToolsSaysSo(t *testing.T) {
	info := testInfo
	info.Tools = nil
	m := newSized(t, Options{Runner: scriptRunner(), Info: info}, 120, 40)
	submit(m, "/tools")
	mustContain(t, plain(m), "可用终端工具: 无")
}

func TestSlashQuitAndExit(t *testing.T) {
	for _, c := range []string{"/quit", "/exit", "/quit now"} {
		m := newSized(t, Options{Runner: scriptRunner()}, 80, 24)
		m.in.SetValue(c)
		if _, cmd := m.Update(keyEnter); !isQuit(cmd) {
			t.Errorf("%q 应退出", c)
		}
	}
}

func TestSlashWorksWhileRunningAndWhenDisabled(t *testing.T) {
	m := newSized(t, Options{Disabled: "没有 Key"}, 100, 70)
	m.running = true
	submit(m, "/help")
	mustContain(t, plain(m), "快捷键")
}

func TestUnknownSlashGoesToModel(t *testing.T) {
	// 以 / 开头的路径、未知命令都不是本地命令，原样交给模型。
	for _, text := range []string{"/etc/hosts 是什么", "/unknown", "/helpme"} {
		m := newSized(t, Options{Runner: scriptRunner()}, 80, 24)
		m.in.SetValue(text)
		if _, cmd := m.Update(keyEnter); cmd == nil || !m.running {
			t.Errorf("%q 应作为普通输入发给模型", text)
		}
	}
}

func fillTranscript(m *Model, n int) {
	for i := 0; i < n; i++ {
		m.add(&entry{kind: kindAssistant, text: fmt.Sprintf("第 %d 行内容", i)})
	}
	m.refresh()
}

func TestFollowsBottomWhileStreaming(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 24)
	fillTranscript(m, 100)
	begin(m, "q")
	apply(m, event.MessageDelta{Text: "最新的回答"})
	mustContain(t, plain(m), "最新的回答")
}

func TestScrollUpPausesFollowAndPgDnResumes(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 24)
	fillTranscript(m, 100)
	m.Update(keyPgUp)
	if m.follow {
		t.Fatal("上滚后应暂停跟随")
	}
	before := plain(m)
	apply(m, event.MessageDelta{Text: "新增内容XYZ"})
	if strings.Contains(plain(m), "新增内容XYZ") {
		t.Error("暂停跟随时新内容不应把视图拉回底部")
	}
	if plain(m) == "" || before == "" {
		t.Fatal("画面为空")
	}
	for i := 0; i < 50 && !m.follow; i++ {
		m.Update(keyPgDn)
	}
	if !m.follow {
		t.Fatal("滚回底部后应恢复跟随")
	}
	mustContain(t, plain(m), "新增内容XYZ")
}

func TestSubmitResumesFollow(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 24)
	fillTranscript(m, 100)
	m.Update(keyPgUp)
	m.in.SetValue("再问一个")
	m.Update(keyEnter)
	if !m.follow {
		t.Fatal("发送新消息应回到底部")
	}
	mustContain(t, plain(m), "再问一个")
}

func TestStaleTurnEventsAreIgnored(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 80, 24)
	begin(m, "q")
	m.Update(eventMsg{turn: m.turn - 1, ev: event.MessageDelta{Text: "上一轮遗留"}})
	m.Update(turnClosedMsg{turn: m.turn - 1})
	if countKind(m, kindAssistant) != 0 || !m.running {
		t.Fatal("旧一轮的消息不应影响当前轮")
	}
}

func TestTurnFinishedNotices(t *testing.T) {
	cases := map[string]string{
		event.ReasonCancelled: "已取消本轮对话",
		event.ReasonLength:    "长度上限截断",
	}
	for reason, want := range cases {
		m := newSized(t, Options{Runner: scriptRunner()}, 80, 24)
		begin(m, "q")
		apply(m, event.TurnFinished{Reason: reason})
		if m.running {
			t.Errorf("%s: 结束后应回到空闲", reason)
		}
		mustContain(t, plain(m), want)
		mustBeIdle(t, m)
	}
}

func TestErrorEventShownAndTurnEnds(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 24)
	begin(m, "q")
	apply(m, event.Error{Message: "认证失败（HTTP 401）"}, event.TurnFinished{Reason: event.ReasonError})
	mustContain(t, plain(m), "✗ 错误：认证失败（HTTP 401）")
	mustBeIdle(t, m)
}

func TestTurnClosedWithoutFinishedDoesNotHang(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 24)
	begin(m, "q")
	m.Update(turnClosedMsg{turn: m.turn})
	if m.running {
		t.Fatal("通道关闭却没有 TurnFinished 时，界面不应一直停在生成中")
	}
	mustContain(t, plain(m), "意外结束")
}

func TestUsageAccumulatesInStatusBar(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 120, 24)
	begin(m, "q")
	apply(m, event.Usage{InputTokens: 100, OutputTokens: 20}, event.Usage{InputTokens: 20, OutputTokens: 10})
	mustContain(t, plain(m), "输入 120", "输出 30")
	apply(m, event.Usage{InputTokens: 2800, OutputTokens: 4000})
	mustContain(t, plain(m), "输入 2.9k", "输出 4k")
}

func TestRetryingShowsNoticeWithCountdown(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 100, 30)
	begin(m, "q")
	apply(m, event.Retrying{Attempt: 2, Max: 3, Delay: 2300 * time.Millisecond, Reason: "服务端过载"})
	mustContain(t, plain(m), "请求失败，3 秒后重试（2/3）：服务端过载")
	if !m.running {
		t.Error("重试期间这一轮仍在进行")
	}
}

func TestCompactedNoticesExplainWhatHappened(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 120, 40)
	begin(m, "q")
	apply(m,
		event.Compacted{Mode: event.CompactPrune, Dropped: 3, TokensBefore: 9000, TokensAfter: 4000},
		event.Compacted{Mode: event.CompactSummary, Dropped: 12, TokensBefore: 8000, TokensAfter: 2500},
		event.Compacted{Mode: event.CompactSummary, TokensBefore: 8000, Err: "模型返回了空摘要"},
	)
	mustContain(t, plain(m),
		"已裁剪 3 条较早的工具输出", "约 9k → 4k token",
		"更早的 12 条消息总结成摘要", "约 8k → 2.5k token",
		"生成摘要失败，历史保持原样继续", "模型返回了空摘要")
}
