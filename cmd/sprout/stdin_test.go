package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRunPromptText(t *testing.T) {
	cases := []struct {
		name      string
		prompt    string
		explicit  bool
		hasImages bool
		stdin     *string // nil 表示 stdin 不是管道或文件
		want      string
		usage     bool
	}{
		{"有提问不读 stdin", "你好", false, false, ptr("不该被读到"), "你好", false},
		{"没提问时读 stdin", "", false, false, ptr("问题在这里\n"), "问题在这里", false},
		{"写了 - 接在提问后面", "审一下", true, false, ptr("diff 内容\n\n"), "审一下\n\ndiff 内容", false},
		{"只有 -", "", true, false, ptr("q"), "q", false},
		{"有图没文字不读 stdin", "", false, true, ptr("不该被读到"), "", false},
		{"写了 - 但 stdin 是空的", "审一下", true, false, ptr("  \n"), "审一下", false},
		{"没提问也没重定向", "", false, false, nil, "", true},
		{"写了 - 却没重定向", "审一下", true, false, nil, "", true},
		{"stdin 是空的又没提问", "", false, false, ptr("\n"), "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var in *strings.Reader
			if c.stdin != nil {
				in = strings.NewReader(*c.stdin)
			}
			var got string
			var err error
			if in == nil {
				got, err = runPromptText(c.prompt, c.explicit, c.hasImages, nil)
			} else {
				got, err = runPromptText(c.prompt, c.explicit, c.hasImages, in)
			}
			if c.usage {
				if !errors.Is(err, errUsage) {
					t.Fatalf("应是用法错误，得到 %q %v", got, err)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("得到 %q %v，期望 %q", got, err, c.want)
			}
		})
	}
}

func ptr(s string) *string { return &s }

// git diff > diff.txt; sprout run 审一下 - < diff.txt：stdin 的内容接在提问后面发给模型。
func TestRunAppendsStdinEndToEnd(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"看完了\"}}]}\n\n"+
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	writeServerConfig(t, dir, srv.URL)

	p := filepath.Join(t.TempDir(), "diff.txt")
	if err := os.WriteFile(p, []byte("diff 内容\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out, errOut bytes.Buffer
	code := realMain([]string{"--data-dir", dir, "run", "审一下", "-"}, f, &out, &errOut)
	if code != 0 || out.String() != "看完了\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 || !strings.Contains(bodies[0], `审一下\n\ndiff 内容`) {
		t.Fatalf("请求里应是提问加 stdin 内容: %v", bodies)
	}
}

// run -i 从 stdin 逐行提问，同一个会话里连续对话：第二次请求带着第一轮的问答。
func TestRunInteractiveEndToEnd(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"好的\"}}]}\n\n"+
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	writeServerConfig(t, dir, srv.URL)

	p := filepath.Join(t.TempDir(), "lines.txt")
	if err := os.WriteFile(p, []byte("第二问\n/exit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out, errOut bytes.Buffer
	code := realMain([]string{"--data-dir", dir, "run", "-i", "第一问"}, f, &out, &errOut)
	if code != 0 || out.String() != "好的\n好的\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 || !strings.Contains(bodies[1], "第一问") || !strings.Contains(bodies[1], "第二问") {
		t.Fatalf("第二次请求应带着第一轮的历史: %d 次请求", len(bodies))
	}
}

// 重定向进来的文件算作可读的 stdin；pipedStdin 只看文件类型，不读内容。
func TestPipedStdinAcceptsRegularFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "q.txt")
	if err := os.WriteFile(p, []byte("q"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if pipedStdin(f) == nil {
		t.Error("普通文件应视为重定向的 stdin")
	}
	if pipedStdin(nil) != nil {
		t.Error("nil 表示没有 stdin")
	}
}

// 空设备是字符设备，不当作有内容的 stdin（CI、IDE 里常把 stdin 接到这里）。
func TestPipedStdinRejectsNullDevice(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Skip("打不开空设备:", err)
	}
	defer f.Close()
	if pipedStdin(f) != nil {
		t.Errorf("%s 不应视为重定向的 stdin", os.DevNull)
	}
}
