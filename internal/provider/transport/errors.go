package transport

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shumengya/sprout/internal/agent"
)

// ParseAPIError 把非 2xx 响应归一化成 ProviderError，适用于 OpenAI 风格
// （{"error":{"message","type","code"}}）。Anthropic 与 Gemini 有自己的风格，
// 由各自包提供 ParseError 覆盖。
func ParseAPIError(status int, body []byte) *agent.ProviderError {
	pe := &agent.ProviderError{Status: status, Retryable: RetryableStatus(status)}
	var env struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Code    json.RawMessage `json:"code"`
	}
	if json.Unmarshal(body, &env) == nil {
		pe.Message, pe.Code = DecodeErrorValue(env.Error)
		if pe.Message == "" {
			pe.Message = env.Message
		}
		if pe.Code == "" {
			pe.Code = RawToString(env.Code)
		}
	}
	if pe.Message == "" {
		pe.Message = ClipText(TrimSpaceBytes(body), 500)
	}
	// 额度用尽之类的错误即使状态码是 429，重试也不会好，白等只会拖慢报错。
	if pe.Code == "insufficient_quota" {
		pe.Retryable = false
	}
	return pe
}

// ParseRetryAfter 解析 Retry-After 头：秒数或 HTTP 日期。解析失败或为负返回 0。
func ParseRetryAfter(v string, now time.Time) time.Duration {
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

// RetryableStatus 是“重试可能成功”的 HTTP 状态码集合。
func RetryableStatus(status int) bool {
	return status == 408 || status == 409 || status == 425 || status == 429 || status >= 500
}

// DecodeErrorValue 解析 error 字段，它可能是对象，也可能直接是字符串。
func DecodeErrorValue(raw json.RawMessage) (msg, code string) {
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
	code = RawToString(obj.Code)
	if code == "" {
		code = obj.Type
	}
	return obj.Message, code
}

// RawToString 把 JSON 的字符串或数字转成文本，null 和其他类型返回空串。
func RawToString(raw json.RawMessage) string {
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

// BodyError 把非 2xx 响应归一化成 ProviderError：先用 parse 解析，
// 解析不出内容时退回原始响应体。
func BodyError(status int, body []byte, parse func(body []byte) (msg, code string)) *agent.ProviderError {
	pe := &agent.ProviderError{Status: status, Retryable: RetryableStatus(status)}
	pe.Message, pe.Code = parse(body)
	if pe.Message == "" {
		pe.Message = ClipText(TrimSpaceBytes(body), 500)
	}
	return pe
}

// NewCallID 为服务端没有给 id 的 tool call 生成一个 id。
func NewCallID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "call_" + hex.EncodeToString(b[:])
}

// StreamError 构造流内错误（HTTP 200 之后在事件里报告的失败）。
func StreamError(status int, code, msg string) *agent.ProviderError {
	return &agent.ProviderError{Status: status, Code: code, Message: msg}
}

// Interrupted 构造“流提前结束/中断”错误，可重试。
func Interrupted(cause error, msg string) *agent.ProviderError {
	return &agent.ProviderError{Message: msg, Retryable: true, Err: cause}
}
