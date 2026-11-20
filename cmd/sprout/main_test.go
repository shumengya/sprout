package main

import (
	"bufio"
	"bytes"
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
	"github.com/shumengya/sprout/internal/agent/platform"
)

const e2eMarker = "sprout-e2e-ok"

// modelServer 扮演 Chat Completions 服务：第一次请求要求调用工具，第二次给最终答案。
type modelServer struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []string
}

func newModelServer(t *testing.T, toolName string) *modelServer {
	t.Helper()
	ms := &modelServer{}
	call, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
		map[string]any{"index": 0, "id": "call_1", "type": "function", "function": map[string]any{
			"name": toolName, "arguments": `{"command":"echo ` + e2eMarker + `"}`}}}}}}})
	ms.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ms.mu.Lock()
		n := len(ms.bodies)
		ms.bodies = append(ms.bodies, string(b))
		ms.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 0 {
			io.WriteString(w, "data: "+string(call)+"\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"命令输出了 "+e2eMarker+"\"}}]}\n\n"+
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(ms.Close)
	return ms
}

// newModelServerStatus 对所有请求返回固定的状态码和响应体。
func newModelServerStatus(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// writeServerConfig 写一份指向 baseURL 的 models.jsonc，apiKey 直接写在文件里。
func writeServerConfig(t *testing.T, dir, baseURL string) {
	t.Helper()
	cfg := strings.NewReplacer(
		`"baseUrl": "https://api.deepseek.com"`, `"baseUrl": "`+baseURL+`"`,
		`"apiKey": ""`, `"apiKey": "sk-e2e"`,
	).Replace(config.Template)
	// 模板改动后替换可能静默失效，那样测试会去请求真实的 DeepSeek，所以必须显式检查。
	if !strings.Contains(cfg, `"baseUrl": "`+baseURL+`"`) || !strings.Contains(cfg, "sk-e2e") {
		t.Fatal("模板里的 deepseek baseUrl / apiKey 没有被替换，请同步更新本测试的替换规则")
	}
	if err := os.WriteFile(config.ModelsPath(dir), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

func run(argv ...string) (code int, stdout, stderr string) {
	var o, e bytes.Buffer
	code = realMain(argv, nil, &o, &e)
	return code, o.String(), e.String()
}

func readJSONL(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("会话文件有非法行: %v", err)
		}
		out = append(out, m)
	}
	return out
}

func TestRunOnceEndToEnd(t *testing.T) {
	sh := platform.DetectShells("", "")
	toolName := "bash"
	if sh.PowerShell != nil {
		toolName = "powershell"
	} else if sh.Bash == nil {
		t.Skip("本机既没有 PowerShell 也没有 bash")
	}
	srv := newModelServer(t, toolName)
	dir := t.TempDir()
	writeServerConfig(t, dir, srv.URL)

	code, out, errOut := run("--data-dir", dir, "run", "看一下", "输出")

	if code != 0 || out != "命令输出了 "+e2eMarker+"\n" {
		t.Fatalf("code=%d\nstdout=%q\nstderr=%q", code, out, errOut)
	}
	for _, want := range []string{"[" + toolName + "] echo " + e2eMarker, "exit 0"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr 缺少 %q: %q", want, errOut)
		}
	}
	if len(srv.bodies) != 2 || !strings.Contains(srv.bodies[1], e2eMarker) || !strings.Contains(srv.bodies[1], "exit_code: 0") {
		t.Fatalf("第二次请求应带着真实命令输出，得到 %d 次请求", len(srv.bodies))
	}
	if !strings.Contains(srv.bodies[0], "看一下 输出") || !strings.Contains(srv.bodies[0], "Sprout") {
		t.Errorf("第一次请求应带用户提问和系统提示词: %.300s", srv.bodies[0])
	}
	checkSessionFiles(t, dir)
}

func checkSessionFiles(t *testing.T, dir string) {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "sessions", "*", "*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("应生成 1 个会话文件，得到 %v", files)
	}
	var types []string
	for _, r := range readJSONL(t, files[0]) {
		types = append(types, r["type"].(string))
	}
	if got := strings.Join(types, ","); got != "session,user,assistant,tool,assistant,turn" {
		t.Errorf("会话记录类型 = %s", got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(files[0]), "meta.json")); err != nil {
		t.Errorf("同目录应有 meta.json: %v", err)
	}
	logs, _ := filepath.Glob(filepath.Join(dir, "logs", "sprout-*.log"))
	if len(logs) != 1 {
		t.Fatalf("应生成日志文件，得到 %v", logs)
	}
	if b, _ := os.ReadFile(logs[0]); !strings.Contains(string(b), "msg=start") || strings.Contains(string(b), "sk-e2e") {
		t.Errorf("日志应记录启动信息，且绝不能含 API Key: %s", b)
	}
}
