package mcp

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// rpcResponse 是一条 JSON-RPC 2.0 响应。ID 用 json.RawMessage 是因为服务端可能回数字也可能回字符串。
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// rpcError 是 JSON-RPC 错误对象。
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return e.Message }

// replyMessage 把处理结果包成给服务端的 JSON-RPC 响应。
func replyMessage(id json.RawMessage, result any, rerr *rpcError) map[string]any {
	msg := map[string]any{"jsonrpc": "2.0", "id": id}
	if rerr != nil {
		msg["error"] = map[string]any{"code": rerr.Code, "message": rerr.Message}
	} else {
		msg["result"] = result
	}
	return msg
}

// parseID 把响应里的 id 转成我们生成请求时用的 uint64。字符串形式的数字也认。
func parseID(raw json.RawMessage) (uint64, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return 0, false
		}
		id, err := strconv.ParseUint(s, 10, 64)
		return id, err == nil
	}
	id, err := strconv.ParseUint(string(raw), 10, 64)
	return id, err == nil
}
