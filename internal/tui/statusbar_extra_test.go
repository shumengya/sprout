package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/shumengya/sprout/internal/agent/event"
)

func TestProcessMemoryIsMergedIntoTheMemorySegment(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 200, 24)
	if line := infoText(m, 0); strings.Contains(line, "内存") {
		t.Errorf("还没采样到时不显示内存: %q", line)
	}
	m.res = Resources{Mem: 63, CPU: 1, ProcMB: 48.4}
	if line := infoText(m, 0); !strings.Contains(line, "内存 63% · 48MB") {
		t.Errorf("整机占用后面应跟上进程自己的 MB: %q", line)
	}
	m.res.ProcMB = 2048
	if line := infoText(m, 0); !strings.Contains(line, "内存 63% · 2.0GB") {
		t.Errorf("超过 1024MB 应换成 GB: %q", line)
	}
	// 读不到进程内存时只写百分比，不留一个孤零零的分隔点。
	m.res.ProcMB = -1
	if line := infoText(m, 0); !strings.Contains(line, "内存 63%") || strings.Contains(line, "63% ·") {
		t.Errorf("没有进程内存时应只写百分比: %q", line)
	}
}

func TestOutputSpeedUsesTheLastCall(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 200, 24)
	if line := infoText(m, 2); !strings.Contains(line, "速度 — token/s") {
		t.Errorf("没有数据时速度写占位符: %q", line)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	apply(m, event.MessageDelta{Text: "a"})
	now = now.Add(2 * time.Second)
	apply(m, event.MessageCompleted{Content: "a"}, event.Usage{OutputTokens: 100})
	if line := infoText(m, 2); !strings.Contains(line, "速度 50.0 token/s") {
		t.Errorf("100 token / 2 秒应是 50.0 token/s: %q", line)
	}
	// 非流式服务没有增量：保持上一次的值，不编造数字。
	apply(m, event.MessageCompleted{Content: "b"}, event.Usage{OutputTokens: 999})
	if line := infoText(m, 2); !strings.Contains(line, "速度 50.0 token/s") {
		t.Errorf("没有增量时应保持原值: %q", line)
	}
	// 速度在第三行，第二行不该再被它占位。
	if line := infoText(m, 1); strings.Contains(line, "速度") {
		t.Errorf("第二行不应显示速度: %q", line)
	}
}

func TestGitSegmentShowsBranchAndCounts(t *testing.T) {
	m := newSized(t, Options{Runner: scriptRunner()}, 200, 24)
	if line := infoText(m, 2); strings.Contains(line, "Git") {
		t.Errorf("没读到时不显示 Git: %q", line)
	}
	m.git = GitInfo{Branch: "main", Added: 1, Modified: 2, Deleted: 3, OK: true}
	if line := infoText(m, 2); !strings.Contains(line, "Git main +1~2-3") {
		t.Errorf("应显示分支和新增/修改/删除数: %q", line)
	}
	// 有上游时在分支名后面带上领先 / 落后；为 0 的不写。
	m.git.Ahead, m.git.Behind = 7, 0
	if line := infoText(m, 2); !strings.Contains(line, "Git main ↑7 +1~2-3") {
		t.Errorf("应显示领先上游的提交数: %q", line)
	}
	m.git.Ahead, m.git.Behind = 0, 2
	if line := infoText(m, 2); !strings.Contains(line, "Git main ↓2 +1~2-3") {
		t.Errorf("应显示落后上游的提交数: %q", line)
	}
}

func TestGitTickStoresInfoAndSchedulesTheNext(t *testing.T) {
	calls := 0
	o := Options{Runner: scriptRunner(), Git: func() GitInfo { calls++; return GitInfo{Branch: "dev", OK: true} }}
	m := newSized(t, o, 200, 24)
	msg := m.gitCmd(0)()
	_, next := m.Update(msg)
	if calls != 1 || m.git.Branch != "dev" || next == nil {
		t.Errorf("calls=%d git=%+v next=%v", calls, m.git, next != nil)
	}
}
