package openai

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

// reply 是服务端对第 N 次请求的回应；status 为 0 表示 200。
type reply struct {
	status int
	body   string
}

func okBody(body string) reply { return reply{body: body} }

type fakeServer struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []captured
}

// newServer 按顺序回放 replies，并记录收到的请求。只用于测试。
func newServer(t *testing.T, replies ...reply) *fakeServer {
	t.Helper()
	fs := &fakeServer{}
	fs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		fs.mu.Lock()
		i := len(fs.reqs)
		fs.reqs = append(fs.reqs, captured{r.URL.Path, r.Header.Clone(), b})
		fs.mu.Unlock()
		if i >= len(replies) {
			http.Error(w, "没有更多预设回应", http.StatusInternalServerError)
			return
		}
		rp := replies[i]
		if rp.status == 0 {
			rp.status = http.StatusOK
		}
		if rp.status/100 == 2 {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		w.WriteHeader(rp.status)
		io.WriteString(w, rp.body)
	}))
	t.Cleanup(fs.Close)
	return fs
}

// sse 把每条 data 编码成一个 SSE 事件。
func sse(data ...string) string {
	var b strings.Builder
	for _, d := range data {
		b.WriteString("data: " + d + "\n\n")
	}
	return b.String()
}

func newProvider(t *testing.T, protocol string, srv *fakeServer) agent.Provider {
	t.Helper()
	p, err := New(Config{Protocol: protocol, BaseURL: srv.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func userReq(text string) agent.Request {
	return agent.Request{
		Model: "m", System: "SYS", MaxOutputTokens: 77,
		Messages: []agent.Message{{Role: agent.RoleUser, Content: []agent.Block{agent.TextBlock(text)}}},
	}
}

// collect 运行一次 Stream，返回响应、收到的全部文本增量和错误。
func collect(p agent.Provider, req agent.Request) (agent.Response, []string, error) {
	var deltas []string
	resp, err := p.Stream(context.Background(), req, func(c agent.Chunk) error {
		deltas = append(deltas, c.TextDelta)
		return nil
	})
	return resp, deltas, err
}

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }
func asList(v any) []any         { l, _ := v.([]any); return l }
