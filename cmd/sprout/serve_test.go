package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestServeEndToEnd 走真实装配：models.jsonc 指向 httptest 服务，经 stdin / stdout 完成两轮对话和一次新会话。
func TestServeEndToEnd(t *testing.T) {
	srv := newEchoServer(t)
	dir := t.TempDir()
	writeServerConfig(t, dir, srv.URL)
	ap, err := buildApp(dir, testEnv(nil, shellSet(false, false)), nil)
	if err != nil || ap.ag == nil {
		t.Fatalf("buildApp: %v disabled=%q", err, ap.disabled)
	}
	defer ap.close()

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	var stderr bytes.Buffer
	code := make(chan int, 1)
	go func() {
		code <- runServe(ap, "", inR, outW, &stderr)
		outW.Close()
	}()
	lines := make(chan map[string]any, 100)
	go func() {
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				t.Errorf("stdout 混入了非协议内容: %q", sc.Text())
				continue
			}
			lines <- m
		}
	}()
	send := func(s string) {
		if _, err := io.WriteString(inW, s+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	next := func() map[string]any {
		select {
		case m := <-lines:
			return m
		case <-time.After(10 * time.Second):
			t.Fatal("等待输出超时")
			return nil
		}
	}
	// 读到 turn.finished 为止，返回拼起来的正文和结束原因。
	answer := func() (string, string) {
		var b strings.Builder
		for {
			p, _ := next()["params"].(map[string]any)
			switch p["kind"] {
			case "message.delta":
				b.WriteString(p["text"].(string))
			case "turn.finished":
				return b.String(), p["reason"].(string)
			}
		}
	}

	send(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	if r := next()["result"].(map[string]any); r["available"] != true || r["model"] != ap.sel.Ref() {
		t.Fatalf("initialize: %v", r)
	}
	send(`{"jsonrpc":"2.0","id":2,"method":"turn/start","params":{"text":"我叫小明"}}`)
	next()
	if text, reason := answer(); text != "回答1" || reason != "stop" {
		t.Fatalf("第一轮: %q %q", text, reason)
	}
	send(`{"jsonrpc":"2.0","id":3,"method":"session/new"}`)
	if m := next(); m["error"] != nil {
		t.Fatalf("session/new: %v", m)
	}
	send(`{"jsonrpc":"2.0","id":4,"method":"turn/start","params":{"text":"我叫什么"}}`)
	next()
	if text, _ := answer(); text != "回答2" {
		t.Fatalf("第二轮: %q", text)
	}
	srv.mu.Lock()
	second := srv.bodies[1]
	srv.mu.Unlock()
	if strings.Contains(second, "小明") {
		t.Error("session/new 之后不应带上旧会话的历史")
	}
	// 列出会话，恢复第一个（非当前的）会话，重放事件里应有当时的提问，下一轮请求应带上它的历史。
	send(`{"jsonrpc":"2.0","id":5,"method":"session/list"}`)
	list := next()["result"].(map[string]any)["sessions"].([]any)
	if len(list) != 2 {
		t.Fatalf("session/list 应有两个会话: %v", list)
	}
	var old map[string]any
	for _, s := range list {
		if s := s.(map[string]any); s["current"] != true {
			old = s
		}
	}
	if old == nil {
		t.Fatalf("应有一个非当前会话: %v", list)
	}
	send(`{"jsonrpc":"2.0","id":6,"method":"session/resume","params":{"selector":"` + old["id"].(string) + `"}}`)
	r := next()["result"].(map[string]any)
	if evs := r["events"].([]any); len(evs) == 0 || evs[0].(map[string]any)["prompt"] != "我叫小明" {
		t.Fatalf("恢复后的重放事件不对: %v", r)
	}
	send(`{"jsonrpc":"2.0","id":7,"method":"turn/start","params":{"text":"我叫什么"}}`)
	next()
	answer()
	srv.mu.Lock()
	third := srv.bodies[2]
	srv.mu.Unlock()
	if !strings.Contains(third, "小明") {
		t.Error("恢复会话后应带上原会话的历史")
	}
	send(`{"jsonrpc":"2.0","id":8,"method":"shutdown"}`)
	next()
	if c := <-code; c != 0 || stderr.Len() != 0 {
		t.Fatalf("退出码 %d stderr=%q", c, stderr.String())
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "sessions", "*", "*.jsonl")); len(files) != 2 {
		t.Errorf("新会话应写到另一个文件: %v", files)
	}
}
