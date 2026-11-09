package openai

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shumengya/sprout/internal/agent"
)

// parseAPIError 把非 2xx 响应归一化成 ProviderError。
// OpenAI / DeepSeek 等返回 {"error":{"message","type","code"}}，
// 个别兼容服务返回 {"error":"文本"} 或 {"message":"文本"}，都要能解析；解析不出就用原始响应体。
func parseAPIError(status int, body []byte) *agent.ProviderError {
	pe := &agent.ProviderError{Status: status, Retryable: retryableStatus(status)}
	var env struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Code    json.RawMessage `json:"code"`
	}
	if json.Unmarshal(body, &env) == nil {
		pe.Message, pe.Code = decodeErrorValue(env.Error)
		if pe.Message == "" {
			pe.Message = env.Message
		}
		if pe.Code == "" {
			pe.Code = rawToString(env.Code)
		}
	}
	if pe.Message == "" {
		pe.Message = clipText(strings.TrimSpace(string(body)), 500)
	}
	// 额度用尽之类的错误即使状态码是 429，重试也不会好，白等只会拖慢报错。
	if pe.Code == "insufficient_quota" {
		pe.Retryable = false
	}
	return pe
}

// parseRetryAfter 解析 Retry-After 头：秒数或 HTTP 日期。解析失败或为负返回 0。
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(max(secs, 0)) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), 0)
	}
	return 0
}

func retryableStatus(status int) bool {
	return status == 408 || status == 409 || status == 425 || status == 429 || status >= 500
}

// decodeErrorValue 解析 error 字段，它可能是对象，也可能直接是字符串。
func decodeErrorValue(raw json.RawMessage) (msg, code string) {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return "", ""
	}
	if raw[0] == '"' {
		var s string
		_ = json.Unmarshal(raw, &s)
		return s, ""
	}
	var obj struct {
		Message string          `json:"message"`
		Code    json.RawMessage `json:"code"`
		Type    string          `json:"type"`
	}
	if json.Unmarshal(raw, &obj) != nil {
		return "", ""
	}
	code = rawToString(obj.Code)
	if code == "" {
		code = obj.Type
	}
	return obj.Message, code
}

// rawToString 把 JSON 的字符串或数字转成文本，null 和其他类型返回空串。
func rawToString(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	switch {
	case s == "" || s == "null":
		return ""
	case s[0] == '"':
		var out string
		_ = json.Unmarshal(raw, &out)
		return out
	case s[0] == '{' || s[0] == '[':
		return ""
	}
	return s
}

// clipText 按字符截断，保证不切坏 UTF-8。
func clipText(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// newCallID 为服务端没有给 id 的 tool call 生成一个 id。
func newCallID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "call_" + hex.EncodeToString(b[:])
}

// streamError 构造流内错误（HTTP 200 之后在事件里报告的失败）。
func streamError(status int, code, msg string) *agent.ProviderError {
	return &agent.ProviderError{Status: status, Code: code, Message: msg}
}

// interrupted 构造“流提前结束/中断”错误，可重试。
func interrupted(cause error, msg string) *agent.ProviderError {
	return &agent.ProviderError{Message: msg, Retryable: true, Err: cause}
}
