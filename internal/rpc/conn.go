// Package rpc 实现 sprout serve：在 stdin / stdout 上按行收发 JSON-RPC 2.0 消息，
// 把内核的事件流转成通知推给客户端。协议细节见 docs/rpc.md。
//
// 本包只依赖内核的事件与输入类型；具体的 agent、会话、配置由调用方通过 Backend 注入。
package rpc

import (
	"encoding/json"
	"fmt"
)

// 错误码。-32700 到 -32603 是 JSON-RPC 2.0 规定的，-32001 起是本协议自己的。
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603

	CodeBusy          = -32001 // 已有一轮对话或压缩在进行
	CodeUnavailable   = -32002 // 当前不能对话（缺 API Key 等），原因在 message 里
	CodeSession       = -32003 // 会话操作失败：找不到、选择器有歧义、不能删除当前会话等
	CodeCancelled     = -32005 // 操作被 turn/cancel 取消
	CodeCompactFailed = -32006 // 没有可压缩的对话，或生成摘要失败
)

// Error 是响应里的 error 对象。
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("%d %s", e.Code, e.Message) }

func errorf(code int, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// incoming 是从客户端读到的一行。ID 缺省（nil）表示通知；显式的 null 解出来是 "null"，按请求处理。
// 带 result / error 而没有 method 的是客户端发来的响应：服务端目前不向客户端发请求，直接忽略。
type incoming struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

// response 是发给客户端的响应。ID 不能省略：解析失败时也要写 "id": null。
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// notification 是服务端主动推送的消息（事件、提示），没有 id。
type notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

var nullID = json.RawMessage("null")

// validID 只接受字符串、数字和 null；对象、数组、布尔值都不是合法的 id。
func validID(id json.RawMessage) bool {
	if len(id) == 0 {
		return false
	}
	switch id[0] {
	case '"', 'n', '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return true
	}
	return false
}

// decodeParams 解析 params；缺省或 null 时保持零值。多余字段忽略，便于客户端向前兼容。
func decodeParams(raw json.RawMessage, v any) *Error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return errorf(CodeInvalidParams, "参数无效: %v", err)
	}
	return nil
}
