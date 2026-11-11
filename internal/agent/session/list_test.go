package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shumengya/sprout/internal/agent/event"
)

const listCwd = `D:\SmyProjects\sprout`

// mkSession 在 sessions 目录下造一个会话文件，prompts 是它包含的提问，mod 是最后修改时间。
func mkSession(t *testing.T, root, id string, mod time.Time, prompts ...string) string {
	t.Helper()
	dir := filepath.Join(root, Slug(listCwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, fmt.Sprintf("20260101-000000_%s.jsonl", id))
	body := fmt.Sprintf(`{"type":"session","id":%q,"cwd":%q,"provider":"deepseek","model":"deepseek-flash"}`+"\n", id, listCwd)
	for _, q := range prompts {
		body += fmt.Sprintf(`{"type":"user","content":%q}`+"\n"+`{"type":"assistant","content":"答"}`+"\n"+`{"type":"turn","reason":"stop"}`+"\n", q)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestListNewestFirstUsingModTime(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	// 文件名里的时间戳相同：必须按修改时间排，因为恢复后继续追加不会改文件名。
	mkSession(t, root, "aaaa1111", now.Add(-2*time.Hour), "最旧的")
	mkSession(t, root, "bbbb2222", now, "最新的", "第二问")
	mkSession(t, root, "cccc3333", now.Add(-time.Hour), "中间的")

	list, err := List(root, listCwd)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, b := range list {
		ids = append(ids, b.ID)
	}
	if strings.Join(ids, ",") != "bbbb2222,cccc3333,aaaa1111" {
		t.Fatalf("顺序 = %v", ids)
	}
	if list[0].Prompt != "最新的" || list[0].Turns != 2 || list[0].Model != "deepseek-flash" || list[0].Provider != "deepseek" {
		t.Errorf("摘要 = %+v", list[0])
	}
}

func TestListSkipsEmptySessionsAndOtherDirs(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	mkSession(t, root, "empty000", now) // 没有任何提问
	mkSession(t, root, "real1111", now.Add(-time.Minute), "有内容")
	other := filepath.Join(root, Slug(`D:\别的项目`))
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(other, "x_other999.jsonl"), []byte(`{"type":"session","id":"other999"}`+"\n"+`{"type":"user","content":"别处"}`+"\n"), 0o600)

	list, _ := List(root, listCwd)
	if len(list) != 1 || list[0].ID != "real1111" {
		t.Fatalf("应只列出本目录下有提问的会话: %+v", list)
	}
}

func TestListEmptyAndMissingDir(t *testing.T) {
	list, err := List(t.TempDir(), listCwd)
	if err != nil || len(list) != 0 {
		t.Fatalf("没有会话目录时应返回空列表: %v %v", list, err)
	}
}

func TestListPromptIsOneLineAndTruncated(t *testing.T) {
	root := t.TempDir()
	mkSession(t, root, "long0000", time.Now(), "第一行\n第二行\t"+strings.Repeat("很长", 150)) // 列表按屏宽截断，这里只截极长的提问
	list, _ := List(root, listCwd)
	p := list[0].Prompt
	if strings.ContainsAny(p, "\n\t") || len([]rune(p)) > promptRunes+1 || !strings.HasSuffix(p, "…") {
		t.Errorf("提问预览应是单行且被截断: %q", p)
	}
}

func TestFindSelectors(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	mkSession(t, root, "abc11111", now, "最新")
	mkSession(t, root, "abc22222", now.Add(-time.Hour), "次新")
	mkSession(t, root, "def33333", now.Add(-2*time.Hour), "最旧")

	cases := []struct {
		sel, want string
	}{
		{"", "abc11111"}, {"latest", "abc11111"}, {"  ", "abc11111"},
		{"1", "abc11111"}, {"3", "def33333"},
		{"def", "def33333"}, {"abc2", "abc22222"}, {"abc22222", "abc22222"},
	}
	for _, c := range cases {
		b, err := Find(root, listCwd, c.sel)
		if err != nil || b.ID != c.want {
			t.Errorf("Find(%q) = %q, %v，期望 %s", c.sel, b.ID, err, c.want)
		}
	}
	for sel, want := range map[string]string{
		"0": "没有序号", "9": "没有序号", "zzz": "没有找到", "abc": "匹配到 2 个",
	} {
		if _, err := Find(root, listCwd, sel); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Find(%q) 错误 = %v，应含 %q", sel, err, want)
		}
	}
}

func TestFindWithNoSessions(t *testing.T) {
	if _, err := Find(t.TempDir(), listCwd, ""); err == nil || !strings.Contains(err.Error(), "还没有可恢复的会话") {
		t.Fatalf("得到 %v", err)
	}
}

func TestAttachAppendsToExistingSessionAndLoadSeesBoth(t *testing.T) {
	root := t.TempDir()
	info := Info{Provider: "deepseek", Model: "deepseek-flash", Cwd: listCwd}

	first := NewRecorder(root, info, nil)
	first.Emit(event.TurnStarted{Prompt: "第一次的提问"})
	first.Emit(event.MessageCompleted{Content: "第一次的回答"})
	first.Emit(event.TurnFinished{Reason: event.ReasonStop})
	first.Close()
	path := first.Path()

	// 下一次启动：新的记录器，Attach 到旧文件，而不是新建一个。
	second := NewRecorder(root, info, nil)
	if err := second.Attach(path); err != nil {
		t.Fatal(err)
	}
	second.Emit(event.TurnStarted{Prompt: "恢复后的提问"})
	second.Emit(event.MessageCompleted{Content: "恢复后的回答"})
	second.Emit(event.TurnFinished{Reason: event.ReasonStop})
	second.Close()

	if second.Path() != path {
		t.Fatalf("应继续写同一个文件: %s vs %s", second.Path(), path)
	}
	files, _ := filepath.Glob(filepath.Join(root, Slug(listCwd), "*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("不应新建会话文件: %v", files)
	}
	st, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if roles(st.History) != "user,assistant,user,assistant" || st.Turns != 2 || st.Skipped != 0 {
		t.Fatalf("恢复后的对话应接在原来的后面: %s 轮数 %d", roles(st.History), st.Turns)
	}
	var resumes int
	for _, r := range readRecords(t, path) {
		if r["type"] == "resume" {
			resumes++
		}
	}
	if resumes != 1 {
		t.Errorf("应记录一次 resume，得到 %d", resumes)
	}
}

func TestAttachSwitchesFromOpenFile(t *testing.T) {
	// TUI 里已经产生过会话文件时 /resume：先关掉当前的，再接到选中的那个。
	root := t.TempDir()
	info := Info{Provider: "p", Model: "m", Cwd: listCwd}
	old := mkSession(t, root, "old00000", time.Now().Add(-time.Hour), "旧会话")

	r := NewRecorder(root, info, nil)
	r.Emit(event.TurnStarted{Prompt: "刚开的新会话"})
	current := r.Path()
	if err := r.Attach(old); err != nil {
		t.Fatal(err)
	}
	r.Emit(event.MessageCompleted{Content: "写进旧会话"})
	r.Close()

	if r.Path() != old || r.Path() == current {
		t.Fatalf("应切换到旧会话: %s", r.Path())
	}
	if b, _ := os.ReadFile(old); !strings.Contains(string(b), "写进旧会话") {
		t.Error("之后的事件应写进旧会话")
	}
	if b, _ := os.ReadFile(current); strings.Contains(string(b), "写进旧会话") {
		t.Error("不应再写进刚才那个会话")
	}
}

func TestAttachToMissingFileFails(t *testing.T) {
	r := NewRecorder(t.TempDir(), Info{Cwd: listCwd}, nil)
	if err := r.Attach(filepath.Join(t.TempDir(), "nope.jsonl")); err == nil {
		t.Fatal("文件不存在应报错，而不是悄悄新建")
	}
}
