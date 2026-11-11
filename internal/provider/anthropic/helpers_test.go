package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/shumengya/sprout/internal/agent"
)

// captured 是服务端收到的一次请求。
type captured struct {
	Path   string
	Header http.Header
	Body   []byte
}

func (c captured) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(c.Body, &m); err != nil {
		t.Fatalf("请求体不是合法 JSON: %v\n%s", err, c.Body)
	}
	return m
}

// fakeServer 回放预设的 SSE 响应体，并记录收到的请求。
type fakeServer struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []captured
	body string
	code int
}

func newServer(t *testing.T, code int, body string) *fakeServer {
	t.Helper()
	fs := &fakeServer{body: body, code: code}
	fs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		fs.mu.Lock()
		fs.reqs = append(fs.reqs, captured{r.URL.RequestURI(), r.Header.Clone(), b})
		fs.mu.Unlock()
		if fs.code == 0 || fs.code/100 == 2 {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		if fs.code != 0 {
			w.WriteHeader(fs.code)
		}
		io.WriteString(w, fs.body)
	}))
	t.Cleanup(fs.Close)
	return fs
}

// sse 把 "event: 名称\ndata: JSON" 事件拼成响应体。
func sse(t *testing.T, events ...string) string {
	t.Helper()
	var b strings.Builder
	for _, e := range events {
		name, data, ok := strings.Cut(e, "\x00")
		if !ok {
			t.Fatalf("事件必须写成 name\\0data: %q", e)
		}
		b.WriteString("event: " + name + "\ndata: " + data + "\n\n")
	}
	return b.String()
}

func newProvider(t *testing.T, srv *fakeServer) agent.Provider {
	t.Helper()
	p, err := New(Config{BaseURL: srv.URL, APIKey: "sk-ant"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func userReq(text string) agent.Request {
	return agent.Request{
		Model: "claude-x", System: "SYS", MaxOutputTokens: 77,
		Messages: []agent.Message{{Role: agent.RoleUser, Content: []agent.Block{agent.TextBlock(text)}}},
	}
}

// collect 运行一次 Stream，返回响应、文本与思维链增量、错误。
func collect(t *testing.T, p agent.Provider, req agent.Request) (agent.Response, []string, []string, error) {
	t.Helper()
	var text, reasoning []string
	resp, err := p.Stream(context.Background(), req, func(c agent.Chunk) error {
		if c.TextDelta != "" {
			text = append(text, c.TextDelta)
		}
		if c.ReasoningDelta != "" {
			reasoning = append(reasoning, c.ReasoningDelta)
		}
		return nil
	})
	return resp, text, reasoning, err
}

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }
func asList(v any) []any         { l, _ := v.([]any); return l }
