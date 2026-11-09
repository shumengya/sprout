package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shumengya/sprout/internal/agent/session"
)

func TestParseResumeFlags(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{"无参数", nil, ""},
		{"-c", []string{"-c"}, "latest"},
		{"--continue", []string{"--continue"}, "latest"},
		{"--resume 序号", []string{"--resume", "2"}, "2"},
		{"--resume=前缀", []string{"--resume=ab12"}, "ab12"},
		{"--resume 优先于 -c", []string{"-c", "--resume", "3"}, "3"},
		{"run 之后的 -c", []string{"run", "-c", "接着问"}, "latest"},
		{"run 之前的 -c", []string{"-c", "run", "接着问"}, "latest"},
		{"run 之后的 --resume", []string{"run", "--resume", "1", "接着问"}, "1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, err := parseArgs(c.argv)
			if err != nil || a.resume != c.want {
				t.Fatalf("resume = %q, %v，期望 %q", a.resume, err, c.want)
			}
		})
	}
	if _, err := parseArgs([]string{"--resume"}); err == nil {
		t.Error("--resume 缺少值应报用法错误")
	}
}

// echoServer 对每个请求回一句“回答N”，并记录收到的请求体。扮演 Chat Completions 服务。
type echoServer struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []string
}

func newEchoServer(t *testing.T) *echoServer {
	t.Helper()
	es := &echoServer{}
	es.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		es.mu.Lock()
		es.bodies = append(es.bodies, string(b))
		n := len(es.bodies)
		es.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		reply, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "回答" + string(rune('0'+n))}}}})
		io.WriteString(w, "data: "+string(reply)+"\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(es.Close)
	return es
}

func TestRunContinueCarriesPreviousConversation(t *testing.T) {
	srv := newEchoServer(t)
	dir := t.TempDir()
	writeServerConfig(t, dir, srv.URL)

	if code, out, errOut := run("--data-dir", dir, "run", "我叫小明"); code != 0 || out != "回答1\n" {
		t.Fatalf("第一次: code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut := run("--data-dir", dir, "run", "-c", "我叫什么？")
	if code != 0 || out != "回答2\n" {
		t.Fatalf("第二次: code=%d out=%q err=%q", code, out, errOut)
	}
	if !strings.Contains(errOut, "已恢复会话") {
		t.Errorf("应在 stderr 说明已恢复: %q", errOut)
	}

	second := srv.bodies[1]
	for _, want := range []string{"我叫小明", "回答1", "我叫什么？"} {
		if !strings.Contains(second, want) {
			t.Errorf("第二次请求应带上之前的对话，缺少 %q", want)
		}
	}
	if strings.Contains(srv.bodies[0], "回答1") {
		t.Error("第一次请求不应有历史")
	}

	// 两次运行写进同一个会话文件，而不是各开一个。
	files, _ := filepath.Glob(filepath.Join(dir, "sessions", "*", "*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("应只有 1 个会话文件: %v", files)
	}
	var types []string
	for _, r := range readJSONL(t, files[0]) {
		types = append(types, r["type"].(string))
	}
	want := "session,user,assistant,turn,resume,user,assistant,turn"
	if got := strings.Join(types, ","); got != want {
		t.Errorf("会话记录 = %s，期望 %s", got, want)
	}
}

func TestRunWithoutContinueStartsFreshSession(t *testing.T) {
	srv := newEchoServer(t)
	dir := t.TempDir()
	writeServerConfig(t, dir, srv.URL)
	run("--data-dir", dir, "run", "我叫小明")
	run("--data-dir", dir, "run", "我叫什么？")
	if strings.Contains(srv.bodies[1], "我叫小明") {
		t.Error("不带 -c 时不应带上之前的对话")
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "sessions", "*", "*.jsonl")); len(files) != 2 {
		t.Errorf("各自一个会话文件: %v", files)
	}
}

func TestRunContinueWithNoSessionIsAnError(t *testing.T) {
	// 脚本里 -c 找不到会话时必须失败：悄悄开新会话会得到完全不同的答案。
	srv := newEchoServer(t)
	dir := t.TempDir()
	writeServerConfig(t, dir, srv.URL)
	code, out, errOut := run("--data-dir", dir, "run", "-c", "接着问")
	if code != 1 || out != "" || !strings.Contains(errOut, "恢复会话失败") || !strings.Contains(errOut, "还没有可恢复的会话") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	if len(srv.bodies) != 0 {
		t.Error("恢复失败时不应请求模型")
	}
}

func TestResumeBySelector(t *testing.T) {
	srv := newEchoServer(t)
	dir := t.TempDir()
	writeServerConfig(t, dir, srv.URL)
	run("--data-dir", dir, "run", "第一个会话")
	run("--data-dir", dir, "run", "第二个会话")

	// 序号 2 是更早的那个（列表按最近使用排序，第二个会话最近）。
	code, _, errOut := run("--data-dir", dir, "run", "--resume", "2", "接着第一个")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	last := srv.bodies[len(srv.bodies)-1]
	if !strings.Contains(last, "第一个会话") || strings.Contains(last, "第二个会话") {
		t.Errorf("应恢复到第一个会话的上下文")
	}
	if code, _, errOut := run("--data-dir", dir, "run", "--resume", "9", "x"); code != 1 || !strings.Contains(errOut, "没有序号") {
		t.Errorf("序号越界: code=%d err=%q", code, errOut)
	}
}

func TestResumeWithDifferentModelDropsReasoning(t *testing.T) {
	srv := newEchoServer(t)
	dir := t.TempDir()
	writeServerConfig(t, dir, srv.URL)
	// 造一个“别的模型”写下的会话，里面带思维链。
	cwd, _ := os.Getwd()
	sdir := filepath.Join(dir, "sessions", session.Slug(cwd))
	if err := os.MkdirAll(sdir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"session","id":"old11111","provider":"other","model":"other-model"}` + "\n" +
		`{"type":"user","content":"旧问题"}` + "\n" +
		`{"type":"assistant","content":"旧回答","reasoning":"别家模型的思维链"}` + "\n" +
		`{"type":"turn","reason":"stop"}` + "\n"
	if err := os.WriteFile(filepath.Join(sdir, "20260101-000000_old11111.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	code, _, errOut := run("--data-dir", dir, "run", "-c", "新问题")
	if code != 0 || !strings.Contains(errOut, "已丢弃旧的思维链") {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	req := srv.bodies[0]
	if !strings.Contains(req, "旧回答") || strings.Contains(req, "别家模型的思维链") {
		t.Errorf("应保留旧对话内容，但不把别家的思维链回传给新模型")
	}
}

func TestResumeWithDisabledAgentFails(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	dir := t.TempDir()
	code, _, errOut := run("--data-dir", dir, "run", "-c", "x")
	if code != 1 || !strings.Contains(errOut, "错误:") {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
}
