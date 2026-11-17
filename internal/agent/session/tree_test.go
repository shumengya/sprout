package session

import (
	"os"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent/event"
)

// ask 往记录器里写完整的一轮：提问、文字回复、正常结束。
func ask(r *Recorder, prompt, reply string) {
	r.Emit(event.TurnStarted{Prompt: prompt})
	r.Emit(event.MessageCompleted{Content: reply, StopReason: "stop"})
	r.Emit(event.TurnFinished{Reason: event.ReasonStop})
}

// texts 把历史里的消息压成文字，方便比较。
func texts(st *State) string {
	var out []string
	for _, m := range st.History {
		out = append(out, string(m.Role)+":"+m.Text())
	}
	return strings.Join(out, " | ")
}

func TestBranchReplaysOnlyCurrentPath(t *testing.T) {
	r, _ := newRec(t)
	ask(r, "问1", "答1")
	ask(r, "问2", "答2")
	if err := r.Branch(1); err != nil {
		t.Fatal(err)
	}
	ask(r, "问3", "答3")
	r.Close()

	st, err := Load(r.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := texts(st), "user:问1 | assistant:答1 | user:问3 | assistant:答3"; got != want {
		t.Fatalf("当前分支的历史 = %q，期望 %q", got, want)
	}
	if st.Turns != 2 {
		t.Errorf("当前路径上应有 2 轮，得到 %d", st.Turns)
	}
	var prompts []string
	for _, e := range st.Events {
		if ts, ok := e.(event.TurnStarted); ok {
			prompts = append(prompts, ts.Prompt)
		}
	}
	if strings.Join(prompts, ",") != "问1,问3" {
		t.Errorf("重放事件里的提问 = %v，不该出现别的分支", prompts)
	}

	old, err := LoadAt(r.Path(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := texts(old), "user:问1 | assistant:答1 | user:问2 | assistant:答2"; got != want {
		t.Fatalf("旧分支的历史 = %q，期望 %q", got, want)
	}
	root, err := LoadAt(r.Path(), 0)
	if err != nil || len(root.History) != 0 || root.Turns != 0 {
		t.Fatalf("会话开头应为空历史: %v %v", root, err)
	}
	if _, err := LoadAt(r.Path(), 4); err == nil {
		t.Fatal("不存在的轮次应报错")
	}
}

func TestReadTree(t *testing.T) {
	r, _ := newRec(t)
	ask(r, "问1", "答1")
	ask(r, "问2\n第二行", "答2")
	if err := r.Branch(1); err != nil {
		t.Fatal(err)
	}
	ask(r, "问3", "答3")
	if err := r.Branch(0); err != nil {
		t.Fatal(err)
	}
	ask(r, "重新开头", "答4")
	r.Close()

	tr, err := ReadTree(r.Path())
	if err != nil {
		t.Fatal(err)
	}
	if tr.Current != 4 || len(tr.Nodes) != 4 {
		t.Fatalf("Current=%d nodes=%d", tr.Current, len(tr.Nodes))
	}
	want := []struct {
		parent        int
		prompt, reply string
	}{{0, "问1", "答1"}, {1, "问2\n第二行", "答2"}, {1, "问3", "答3"}, {0, "重新开头", "答4"}}
	for i, w := range want {
		n := tr.Nodes[i]
		if n.Turn != i+1 || n.Parent != w.parent || n.Prompt != w.prompt || n.Reply != w.reply || n.Time.IsZero() {
			t.Errorf("第 %d 轮 = %+v，期望 %+v", i+1, n, w)
		}
	}

	// 切换位置后还没提问：当前位置就是切过去的那一轮。
	r2, _ := newRec(t)
	ask(r2, "问1", "答1")
	ask(r2, "问2", "答2")
	if err := r2.Branch(1); err != nil {
		t.Fatal(err)
	}
	r2.Close()
	tr2, err := ReadTree(r2.Path())
	if err != nil || tr2.Current != 1 {
		t.Fatalf("切换后未提问时 Current 应为 1: %v %v", tr2, err)
	}
	st, _ := Load(r2.Path())
	if st.Turns != 1 || texts(st) != "user:问1 | assistant:答1" {
		t.Fatalf("切换后恢复应停在第 1 轮: %q", texts(st))
	}
}

// 切换位置后、下一次提问前做的压缩只属于新位置：回到原来的分支时不该带上它。
func TestCompactAfterBranchStaysOnNewPath(t *testing.T) {
	r, _ := newRec(t)
	ask(r, "问1", "答1")
	ask(r, "问2", "答2")
	if err := r.Branch(1); err != nil {
		t.Fatal(err)
	}
	r.Emit(event.Compacted{Mode: event.CompactSummary, Dropped: 2, Summary: "摘要", Manual: true})
	ask(r, "问3", "答3")
	r.Close()

	st, err := Load(r.Path())
	if err != nil {
		t.Fatal(err)
	}
	if st.Summary != "摘要" || texts(st) != "user:问3 | assistant:答3" {
		t.Fatalf("新分支应带着压缩结果: summary=%q history=%q", st.Summary, texts(st))
	}
	old, err := LoadAt(r.Path(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if old.Summary != "" || len(old.History) != 4 {
		t.Fatalf("原分支不应受新位置上的压缩影响: summary=%q history=%q", old.Summary, texts(old))
	}
}

func TestBadBranchRecordIsSkipped(t *testing.T) {
	r, _ := newRec(t)
	ask(r, "问1", "答1")
	r.Close()
	f, err := os.OpenFile(r.Path(), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"type":"branch","time":"2026-11-17T10:00:00Z","turn":9}` + "\n")
	f.Close()

	st, err := Load(r.Path())
	if err != nil {
		t.Fatal(err)
	}
	if st.Skipped != 1 || st.Turns != 1 || texts(st) != "user:问1 | assistant:答1" {
		t.Fatalf("指向不存在轮次的 branch 记录应跳过并计数: skipped=%d %q", st.Skipped, texts(st))
	}
}

func TestRecorderBranchNeedsOpenFile(t *testing.T) {
	r, _ := newRec(t)
	if err := r.Branch(0); err == nil {
		t.Fatal("还没有会话文件时 Branch 应报错")
	}
	ask(r, "问1", "答1")
	r.Close()
	if err := r.Branch(0); err == nil {
		t.Fatal("关闭后 Branch 应报错")
	}
}
