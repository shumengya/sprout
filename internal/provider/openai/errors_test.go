package openai

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/config"
)

func TestParseAPIError(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		msg, code string
		retryable bool
	}{
		{"OpenAI 标准格式", 401, `{"error":{"message":"Incorrect API key","type":"invalid_request_error","code":"invalid_api_key"}}`,
			"Incorrect API key", "invalid_api_key", false},
		{"code 为 null 时退回 type", 400, `{"error":{"message":"bad","type":"invalid_request_error","code":null}}`,
			"bad", "invalid_request_error", false},
		{"数字 code", 400, `{"error":{"message":"bad","code":40001}}`, "bad", "40001", false},
		{"error 直接是字符串", 400, `{"error":"model not found"}`, "model not found", "", false},
		{"顶层 message", 400, `{"message":"oops"}`, "oops", "", false},
		{"429 可重试", 429, `{"error":{"message":"slow down","code":"rate_limit_exceeded"}}`, "slow down", "rate_limit_exceeded", true},
		{"500 纯文本", 500, `Internal Server Error`, "Internal Server Error", "", true},
		{"502 HTML 网关页", 502, `<html>Bad Gateway</html>`, "<html>Bad Gateway</html>", "", true},
		{"空响应体", 503, ``, "", "", true},
		{"404 不重试", 404, `{"error":{"message":"The model does not exist"}}`, "The model does not exist", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pe := parseAPIError(c.status, []byte(c.body))
			if pe.Status != c.status || pe.Message != c.msg || pe.Code != c.code || pe.Retryable != c.retryable {
				t.Fatalf("得到 %+v", pe)
			}
		})
	}
}

func TestParseAPIErrorClipsHugeBody(t *testing.T) {
	pe := parseAPIError(500, []byte(strings.Repeat("字", 5000)))
	if n := len([]rune(pe.Message)); n > 501 {
		t.Fatalf("原始响应体应被截断，长度 %d", n)
	}
}

func TestContextLengthErrorGetsFriendlyMessage(t *testing.T) {
	srv := newServer(t, reply{status: 400, body: `{"error":{"message":"maximum context length is 128000 tokens","code":"context_length_exceeded"}}`})
	_, _, err := collect(newProvider(t, config.ProtocolChat, srv), userReq("q"))
	if err == nil || !strings.Contains(err.Error(), "上下文超出模型窗口") {
		t.Fatalf("得到 %v", err)
	}
}

func TestHTTPErrorsAreProviderErrors(t *testing.T) {
	for _, protocol := range []string{config.ProtocolChat, config.ProtocolResponses} {
		srv := newServer(t, reply{status: 429, body: `{"error":{"message":"quota"}}`})
		_, _, err := collect(newProvider(t, protocol, srv), userReq("q"))
		var pe *agent.ProviderError
		if !errors.As(err, &pe) || pe.Status != 429 || !pe.Retryable || !strings.Contains(err.Error(), "额度") {
			t.Errorf("%s: 得到 %v", protocol, err)
		}
	}
}

func TestNetworkFailureIsRetryableProviderError(t *testing.T) {
	srv := newServer(t)
	p := newProvider(t, config.ProtocolChat, srv)
	srv.Close() // 之后的请求会连接失败
	_, _, err := collect(p, userReq("q"))
	var pe *agent.ProviderError
	if !errors.As(err, &pe) || pe.Status != 0 || !pe.Retryable || pe.Err == nil {
		t.Fatalf("网络失败应是可重试的 ProviderError 且保留底层错误，得到 %v", err)
	}
	if !strings.Contains(err.Error(), "通信失败") {
		t.Errorf("提示应是中文: %s", err)
	}
}

func TestAPIKeyNeverAppearsInErrors(t *testing.T) {
	srv := newServer(t, reply{status: 401, body: `{"error":{"message":"bad"}}`})
	p, _ := New(Config{Protocol: config.ProtocolChat, BaseURL: srv.URL, APIKey: "sk-secret-123"})
	_, _, err := collect(p, userReq("q"))
	if err == nil || strings.Contains(err.Error(), "sk-secret-123") {
		t.Fatalf("错误信息不得包含 API Key: %v", err)
	}
}

func TestNewValidates(t *testing.T) {
	if _, err := New(Config{Protocol: config.ProtocolChat, BaseURL: "https://x"}); err == nil {
		t.Error("API Key 为空应报错")
	}
	if _, err := New(Config{Protocol: config.ProtocolChat, BaseURL: "https://x", APIKey: "  "}); err == nil {
		t.Error("API Key 全是空白应报错")
	}
	if _, err := New(Config{Protocol: "anthropic", BaseURL: "https://x", APIKey: "k"}); err == nil {
		t.Error("不支持的协议应报错")
	}
	for _, p := range []string{config.ProtocolChat, config.ProtocolResponses} {
		if _, err := New(Config{Protocol: p, BaseURL: "https://x", APIKey: "k"}); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}

func TestBaseURLTrailingSlashIsTolerated(t *testing.T) {
	srv := newServer(t, okBody(sse(chatDelta("ok"), `{"choices":[{"delta":{},"finish_reason":"stop"}]}`, "[DONE]")))
	p, _ := New(Config{Protocol: config.ProtocolChat, BaseURL: srv.URL + "/v1/", APIKey: "k"})
	if _, _, err := collect(p, userReq("q")); err != nil {
		t.Fatal(err)
	}
	if srv.reqs[0].Path != "/v1/chat/completions" {
		t.Errorf("路径 = %s，不应出现双斜杠", srv.reqs[0].Path)
	}
}

func TestClipText(t *testing.T) {
	if got := clipText("你好世界", 2); got != "你好…" {
		t.Errorf("clipText = %q", got)
	}
	if got := clipText("ab", 5); got != "ab" {
		t.Errorf("clipText = %q", got)
	}
}

func TestNewCallIDUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := newCallID()
		if seen[id] || !strings.HasPrefix(id, "call_") {
			t.Fatalf("id %q 重复或前缀不对", id)
		}
		seen[id] = true
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"":                              0,
		"5":                             5 * time.Second,
		" 12 ":                          12 * time.Second,
		"-3":                            0,
		"abc":                           0,
		"Mon, 27 Jul 2026 12:00:09 GMT": 9 * time.Second,
		"Mon, 27 Jul 2026 11:00:00 GMT": 0, // 已经过去的时间
	}
	for in, want := range cases {
		if got := parseRetryAfter(in, now); got != want {
			t.Errorf("parseRetryAfter(%q) = %v，期望 %v", in, got, want)
		}
	}
}

func TestRetryAfterHeaderReachesProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "4")
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"message":"slow down","code":"rate_limit_exceeded"}}`)
	}))
	defer srv.Close()
	p, _ := New(Config{Protocol: config.ProtocolChat, BaseURL: srv.URL, APIKey: "k"})
	_, _, err := collect(p, userReq("q"))
	var pe *agent.ProviderError
	if !errors.As(err, &pe) || pe.RetryAfter != 4*time.Second || !pe.Retryable {
		t.Fatalf("得到 %+v", pe)
	}
}

func TestInsufficientQuotaIsNotRetryable(t *testing.T) {
	// 额度用尽即使是 429，重试也不会好。
	pe := parseAPIError(429, []byte(`{"error":{"message":"You exceeded your current quota","code":"insufficient_quota"}}`))
	if pe.Retryable {
		t.Fatal("insufficient_quota 不应重试")
	}
	if rl := parseAPIError(429, []byte(`{"error":{"message":"rate","code":"rate_limit_exceeded"}}`)); !rl.Retryable {
		t.Fatal("普通限流应可重试")
	}
}
