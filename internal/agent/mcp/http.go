package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

// maxSSEEvent 是单个 SSE 事件允许的最大字节数，防止服务端异常时无限缓冲。
const maxSSEEvent = 64 << 20

// httpTransport 走 Streamable HTTP 传输：每个 JSON-RPC 请求一次 POST，
// 响应可能是单个 JSON，也可能是一条 SSE 流（流里最后一个匹配 id 的事件才是响应）。
type httpTransport struct {
	spec Spec
	hc   *http.Client

	mu        sync.Mutex
	sessionID string // 旧版协议里服务端下发、之后每个请求都要带上的会话 ID
	version   string // 协商出的协议版本，作为 MCP-Protocol-Version 头
	nextID    atomic.Uint64
	// notifyHook 是服务端通知的处理函数。通知随任意一次请求的 SSE 流一起到达。
	notifyHook atomic.Pointer[func(string)]
}

func (t *httpTransport) setNotifyHook(fn func(string)) {
	if fn == nil {
		return
	}
	t.notifyHook.Store(&fn)
}

// hook 返回当前的通知钩子；没有注册时返回 nil。
func (t *httpTransport) hook() func(string) {
	if p := t.notifyHook.Load(); p != nil {
		return *p
	}
	return nil
}

func newHTTPTransport(spec Spec, hc *http.Client) *httpTransport {
	return &httpTransport{spec: spec, hc: hc}
}

// setVersion 由客户端在握手后调用，给后续请求带上协议版本头。
func (t *httpTransport) setVersion(v string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.version = v
}

func (t *httpTransport) request(ctx context.Context, method, name string, params any) (json.RawMessage, error) {
	id := t.nextID.Add(1)
	body := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": paramsOrEmpty(params)}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	resp, err := t.post(ctx, data, method, name)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.New("会话已失效（HTTP 404），请重启 sprout 重新连接")
	}
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent {
		return nil, fmt.Errorf("%s 没有返回结果（HTTP %d）", method, resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "text/event-stream"):
		return readSSEResponse(resp.Body, id, method, t.hook())
	case strings.HasPrefix(ct, "application/json"):
		return readJSONResponse(resp.Body, method)
	default:
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return nil, httpError(resp, method)
		}
		return nil, fmt.Errorf("%s 返回了无法识别的 Content-Type %q", method, ct)
	}
}

// post 发出一次 POST 并校验状态码；非 2xx 时读一小段响应体放进错误里。
func (t *httpTransport) post(ctx context.Context, data []byte, method, name string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.spec.URL, strings.NewReader(string(data)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range t.spec.Headers {
		req.Header.Set(k, v)
	}
	t.mu.Lock()
	session, version := t.sessionID, t.version
	t.mu.Unlock()
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	if version != "" {
		req.Header.Set("MCP-Protocol-Version", version)
		// 无状态协议要求把方法名和目标名放进头里，网关只读头就能路由与计量。
		if version >= ProtocolStateless {
			req.Header.Set("Mcp-Method", method)
			if name != "" {
				req.Header.Set("Mcp-Name", name)
			}
		}
	}

	resp, err := t.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连接 %s 失败: %w", t.spec.URL, err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
			t.mu.Lock()
			t.sessionID = sid
			t.mu.Unlock()
		}
		return resp, nil
	}
	// 非 2xx：有的服务端仍会给 JSON-RPC 错误体，优先用它。
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		if _, jerr := readJSONResponse(resp.Body, method); jerr == nil {
			resp.Body.Close()
			return nil, fmt.Errorf("%s 返回了 JSON-RPC 结果，但 HTTP 状态是 %d", method, resp.StatusCode)
		} else if !errors.Is(jerr, errNoBody) {
			resp.Body.Close()
			return nil, jerr
		}
	}
	defer resp.Body.Close()
	return nil, httpError(resp, method)
}

var errNoBody = errors.New("响应体里没有结果")

func httpError(resp *http.Response, method string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		return fmt.Errorf("%s 失败：HTTP %d", method, resp.StatusCode)
	}
	return fmt.Errorf("%s 失败：HTTP %d: %s", method, resp.StatusCode, msg)
}

// readJSONResponse 读一条 JSON 响应（可能是单个对象，也可能是批量数组）。
func readJSONResponse(r io.Reader, method string) (json.RawMessage, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxSSEEvent))
	if err != nil {
		return nil, fmt.Errorf("读取 %s 响应失败: %w", method, err)
	}
	data = []byte(strings.TrimSpace(string(data)))
	if len(data) == 0 {
		return nil, errNoBody
	}
	if data[0] == '[' { // 批量响应：取第一条带 result/error 的
		var list []rpcResponse
		if err := json.Unmarshal(data, &list); err != nil {
			return nil, fmt.Errorf("解析 %s 响应失败: %w", method, err)
		}
		if len(list) == 0 {
			return nil, errNoBody
		}
		return unwrapResponse(list[0], method)
	}
	var resp rpcResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("解析 %s 响应失败: %w", method, err)
	}
	return unwrapResponse(resp, method)
}

func unwrapResponse(resp rpcResponse, method string) (json.RawMessage, error) {
	if resp.Error != nil {
		return nil, fmt.Errorf("%s 返回错误 %d: %w", method, resp.Error.Code, resp.Error)
	}
	if len(resp.Result) == 0 {
		return nil, errNoBody
	}
	return resp.Result, nil
}

// readSSEResponse 读完 SSE 流，返回 id 匹配的那条响应。服务端可能先发若干通知，
// 通知交给 hook（工具列表变更等），不影响本次响应的等待。
func readSSEResponse(r io.Reader, id uint64, method string, hook func(string)) (json.RawMessage, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	var data []string
	var size int
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			size += len(line)
			if size > maxSSEEvent {
				return nil, fmt.Errorf("%s 的 SSE 流超过 %d MB", method, maxSSEEvent>>20)
			}
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				if len(data) > 0 {
					if raw, done, derr := matchEvent(strings.Join(data, "\n"), id, method, hook); done || derr != nil {
						return raw, derr
					}
					data, size = nil, 0
				}
			case strings.HasPrefix(line, ":"):
				// 注释行，常被用作心跳
			case strings.HasPrefix(line, "data:"):
				data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
		if err != nil {
			if len(data) > 0 {
				if raw, done, derr := matchEvent(strings.Join(data, "\n"), id, method, hook); done || derr != nil {
					return raw, derr
				}
			}
			if errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("%s 的 SSE 流在返回结果前就结束了", method)
			}
			return nil, fmt.Errorf("读取 %s 的 SSE 流失败: %w", method, err)
		}
	}
}

// matchEvent 判断一条 SSE 事件是不是我们要的响应；是则返回结果，done 为 true。
// 带 method 的事件是服务端通知或请求，交给 hook。
func matchEvent(payload string, id uint64, method string, hook func(string)) (json.RawMessage, bool, error) {
	var resp rpcResponse
	if err := json.Unmarshal([]byte(payload), &resp); err != nil {
		return nil, false, nil // 不是 JSON 的事件，忽略
	}
	if resp.Method != "" {
		if hook != nil && (len(resp.ID) == 0 || string(resp.ID) == "null") {
			go hook(resp.Method)
		}
		return nil, false, nil // 服务端的通知或请求
	}
	got, ok := parseID(resp.ID)
	if !ok || got != id {
		return nil, false, nil
	}
	raw, err := unwrapResponse(resp, method)
	return raw, true, err
}

func (t *httpTransport) notify(ctx context.Context, method string, params any) error {
	body := map[string]any{"jsonrpc": "2.0", "method": method, "params": paramsOrEmpty(params)}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := t.post(ctx, data, method, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

// close 尽量按规范结束会话；服务端不支持 DELETE 时返回 405，忽略即可。
func (t *httpTransport) close() error {
	t.mu.Lock()
	session := t.sessionID
	t.mu.Unlock()
	if session == "" {
		return nil
	}
	req, err := http.NewRequest(http.MethodDelete, t.spec.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Mcp-Session-Id", session)
	resp, err := t.hc.Do(req)
	if err != nil {
		return nil // 关不掉会话不算错误
	}
	resp.Body.Close()
	return nil
}
