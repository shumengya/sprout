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

	"github.com/shumengya/sprout/internal/agent/config"
)

const subReport = "子代理报告：标记在 a.go:7"

// subagentServer 扮演 Chat Completions 服务：系统提示词里带「## 子代理」的是子代理的请求，直接给报告；
// 主代理第一次请求调用 agent 工具，拿到报告后给出最终答案。
type subagentServer struct {
	*httptest.Server
	mu         sync.Mutex
	main, kids []string
}

func sse(w io.Writer, chunks ...any) {
	for _, c := range chunks {
		b, _ := json.Marshal(c)
		io.WriteString(w, "data: "+string(b)+"\n\n")
	}
	io.WriteString(w, "data: [DONE]\n\n")
}

func text(s, finish string) map[string]any {
	return map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": s}, "finish_reason": finish}}}
}

func newSubagentServer(t *testing.T, agentArgs string) *subagentServer {
	t.Helper()
	s := &subagentServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body := string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		s.mu.Lock()
		defer s.mu.Unlock()
		if strings.Contains(body, "## 子代理") {
			s.kids = append(s.kids, body)
			sse(w, text(subReport, "stop"),
				map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 300, "completion_tokens": 12, "total_tokens": 312}})
			return
		}
		s.main = append(s.main, body)
		if len(s.main) == 1 {
			sse(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
				map[string]any{"index": 0, "id": "call_1", "type": "function", "function": map[string]any{"name": "agent", "arguments": agentArgs}},
			}}}}}, text("", "tool_calls"))
			return
		}
		sse(w, text("主代理：已确认", "stop"))
	}))
	t.Cleanup(s.Close)
	return s
}

// requestTools 取出请求里下发的工具名。
func requestTools(t *testing.T, body string) []string {
	t.Helper()
	var req struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, tl := range req.Tools {
		out = append(out, tl.Function.Name)
	}
	return out
}

func TestRunDelegatesToSubagentEndToEnd(t *testing.T) {
	srv := newSubagentServer(t, `{"description":"找标记","prompt":"在仓库里找出标记","agent":"explore"}`)
	dir := t.TempDir()
	writeServerConfig(t, dir, srv.URL)

	code, out, errOut := run("--data-dir", dir, "run", "帮我找标记")
	if code != 0 || out != "主代理：已确认\n" {
		t.Fatalf("code=%d\nstdout=%q\nstderr=%q", code, out, errOut)
	}
	if !strings.Contains(errOut, "[agent] explore: 找标记") {
		t.Errorf("stderr 应显示 agent 调用: %q", errOut)
	}
	if len(srv.kids) != 1 || len(srv.main) != 2 {
		t.Fatalf("主代理请求 %d 次、子代理 %d 次", len(srv.main), len(srv.kids))
	}

	kid := srv.kids[0]
	for _, want := range []string{"在仓库里找出标记", "类型：explore", "### 角色", "只读任务"} {
		if !strings.Contains(kid, want) {
			t.Errorf("子代理请求缺少 %q", want)
		}
	}
	if strings.Contains(kid, "帮我找标记") {
		t.Error("子代理不该看到主对话")
	}
	for _, name := range requestTools(t, kid) {
		if name != "read" && name != "bash" && name != "powershell" {
			t.Errorf("explore 只该拿到 read 与 shell，却拿到了 %s", name)
		}
	}
	if !strings.Contains(srv.main[1], subReport) {
		t.Error("主代理第二次请求应带着子代理的报告")
	}
	if tools := requestTools(t, srv.main[0]); !strings.Contains(strings.Join(tools, ","), "agent") {
		t.Errorf("主代理应能看到 agent 工具: %v", tools)
	}

	// 主会话记下子代理用量；子代理的完整过程在 <会话名>.agents/call_1.jsonl。
	files, _ := filepath.Glob(filepath.Join(dir, "sessions", "*", "*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("会话列表里应只有主会话: %v", files)
	}
	var types []string
	for _, r := range readJSONL(t, files[0]) {
		types = append(types, r["type"].(string))
	}
	if got := strings.Join(types, ","); got != "session,user,assistant,subagent_usage,tool,assistant,turn" {
		t.Errorf("主会话记录 = %s", got)
	}
	child := filepath.Join(strings.TrimSuffix(files[0], ".jsonl")+".agents", "call_1.jsonl")
	recs := readJSONL(t, child)
	if len(recs) == 0 || recs[0]["agent"] != "explore" {
		t.Fatalf("子代理记录 = %v", recs)
	}
	var childTypes []string
	for _, r := range recs {
		childTypes = append(childTypes, r["type"].(string))
	}
	if got := strings.Join(childTypes, ","); got != "session,user,assistant,usage,turn" {
		t.Errorf("子代理记录类型 = %s", got)
	}
}

func TestCustomSubagentDefinitionIsOffered(t *testing.T) {
	srv := newSubagentServer(t, `{"description":"审查","prompt":"看看改动","agent":"reviewer"}`)
	dir := t.TempDir()
	writeServerConfig(t, dir, srv.URL)
	if err := os.MkdirAll(config.SubagentsDir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	def := "---\ndescription: 审查代码改动\ntools: [read]\n---\n只挑真正的 bug。\n"
	if err := os.WriteFile(filepath.Join(config.SubagentsDir(dir), "reviewer.md"), []byte(def), 0o600); err != nil {
		t.Fatal(err)
	}

	code, _, errOut := run("--data-dir", dir, "run", "审查一下")
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(srv.main[0], "reviewer：审查代码改动（工具：read）") {
		t.Error("agent 工具描述里应列出自定义类型")
	}
	if len(srv.kids) != 1 || !strings.Contains(srv.kids[0], "只挑真正的 bug。") {
		t.Fatal("子代理请求应带上自定义的角色说明")
	}
	if tools := requestTools(t, srv.kids[0]); strings.Join(tools, ",") != "read" {
		t.Errorf("reviewer 只该拿到 read: %v", tools)
	}
}

func TestSubagentWithUnknownModelFailsAsToolResult(t *testing.T) {
	srv := newSubagentServer(t, `{"description":"d","prompt":"p","agent":"bad"}`)
	dir := t.TempDir()
	writeServerConfig(t, dir, srv.URL)
	os.MkdirAll(config.SubagentsDir(dir), 0o755)
	os.WriteFile(filepath.Join(config.SubagentsDir(dir), "bad.md"), []byte("---\ndescription: d\nmodel: nope/none\n---\n"), 0o600)

	code, _, errOut := run("--data-dir", dir, "run", "q")
	if code != 0 {
		t.Fatalf("模型写错只让这次调用失败，不该终止主代理: code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(errOut, "model 无效") {
		t.Errorf("启动提示与工具失败都应说明 model 无效: %q", errOut)
	}
	if len(srv.kids) != 0 || !strings.Contains(srv.main[1], "无法启动子代理 bad") {
		t.Errorf("失败原因应回给主代理: kids=%d", len(srv.kids))
	}
}
