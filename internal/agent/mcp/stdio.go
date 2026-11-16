package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shumengya/sprout/internal/agent/platform"
)

// maxLine 是单条 JSON-RPC 消息的上限。工具结果可能很大，给小一点的上限会在读取时截断 JSON。
const maxLine = 64 << 20

// stderrTail 保留多少字节的服务端 stderr，用于出错时附在错误信息里。
const stderrTail = 4 << 10

// closeTimeout 是关闭子进程时等它自行退出的时间，超过就杀进程树。
const closeTimeout = 2 * time.Second

// stdioTransport 以子进程方式与 MCP 服务端通信：每行一条 JSON-RPC 消息。
type stdioTransport struct {
	cmd *exec.Cmd
	// cancel 结束子进程的生命周期（platform.ConfigureCmd 设置的 Cancel 依赖 CommandContext 创建的命令）。
	cancel context.CancelFunc
	stdin  io.WriteCloser
	// stderr 缓冲区，出错时给用户看服务端自己打的日志。
	errBuf *tailBuffer

	writeMu sync.Mutex // stdout 写入必须串行，但不能和响应读取用同一把锁
	pending sync.Map   // id(uint64) -> chan rpcResponse
	nextID  atomic.Uint64
	closed  chan struct{}
	readErr atomic.Value  // error：读循环结束的原因
	wait    chan struct{} // 子进程退出后关闭
	// notifyHook 是服务端通知的处理函数，由 Client 在握手后注册。
	notifyHook atomic.Pointer[func(string)]
}

func newStdioTransport(spec Spec) (*stdioTransport, error) {
	if strings.TrimSpace(spec.Command) == "" {
		return nil, errors.New("stdio 服务端缺少 command")
	}
	// 子进程的生命周期跟着 sprout 走：Context 只在 Close 时取消，超时由每个请求自己控制。
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, spec.Command, spec.Args...)
	cmd.Dir = spec.Dir
	if len(spec.Env) > 0 {
		cmd.Env = append(os.Environ(), envList(spec.Env)...)
	}
	platform.ConfigureCmd(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("创建 stdin 管道失败: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("创建 stdout 管道失败: %w", err)
	}
	errBuf := &tailBuffer{limit: stderrTail}
	cmd.Stderr = errBuf
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("无法启动 %s: %w", spec.Command, err)
	}

	t := &stdioTransport{cmd: cmd, cancel: cancel, stdin: stdin, errBuf: errBuf, closed: make(chan struct{}), wait: make(chan struct{})}
	go t.readLoop(stdout)
	go func() {
		_ = cmd.Wait()
		close(t.wait)
	}()
	return t, nil
}

func envList(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

// readLoop 持续读取 stdout，按 id 把响应分发给等待者，其余消息（通知、服务端发起的请求）忽略。
func (t *stdioTransport) readLoop(stdout io.Reader) {
	defer close(t.closed)
	r := bufio.NewReaderSize(stdout, 64*1024)
	for {
		line, err := readLine(r, maxLine)
		if len(line) > 0 {
			t.dispatch(line)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.readErr.Store(err)
			}
			return
		}
	}
}

// readLine 读一整行，超过上限时返回错误而不是静默截断。
func readLine(r *bufio.Reader, limit int) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > limit {
			return nil, fmt.Errorf("单条 JSON-RPC 消息超过 %d MB", limit>>20)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return buf, err
	}
}

func (t *stdioTransport) dispatch(line []byte) {
	line = []byte(strings.TrimSpace(string(line)))
	if len(line) == 0 {
		return
	}
	var probe struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		return // 不是 JSON 的行（服务端误写到 stdout 的日志）直接丢弃
	}
	if probe.Method != "" {
		// 服务端的通知或发起的请求。本客户端不支持采样/elicitation，能答的随手答掉。
		t.handleServerMessage(line, probe.Method, probe.ID)
		return
	}
	var resp rpcResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return
	}
	id, ok := parseID(resp.ID)
	if !ok {
		return
	}
	if ch, ok := t.pending.LoadAndDelete(id); ok {
		ch.(chan rpcResponse) <- resp
	}
}

// handleServerMessage 回应服务端发来的请求。ping 回空结果，roots/list 回空列表，
// 其余回“方法不支持”，避免服务端一直等。通知（没有 id）交给注册的钩子。
func (t *stdioTransport) handleServerMessage(line []byte, method string, id json.RawMessage) {
	if len(id) == 0 || string(id) == "null" {
		// 通知，无需回应；工具列表变更等由钩子处理。
		if h := t.notifyHook.Load(); h != nil {
			go (*h)(method)
		}
		return
	}
	var result any = map[string]any{}
	switch method {
	case "ping":
	case "roots/list":
		result = map[string]any{"roots": []any{}}
	default:
		_ = t.writeMessage(map[string]any{
			"jsonrpc": "2.0", "id": id,
			"error": map[string]any{"code": -32601, "message": "不支持的方法: " + method},
		})
		return
	}
	_ = t.writeMessage(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (t *stdioTransport) request(ctx context.Context, method, _ string, params any) (json.RawMessage, error) {
	id := t.nextID.Add(1)
	ch := make(chan rpcResponse, 1)
	t.pending.Store(id, ch)
	defer t.pending.Delete(id)

	if err := t.writeMessage(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": paramsOrEmpty(params)}); err != nil {
		return nil, err
	}
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, fmt.Errorf("%s 返回错误 %d: %w", method, resp.Error.Code, resp.Error)
		}
		return resp.Result, nil
	case <-ctx.Done():
		// 通知服务端取消，尽力而为。
		_ = t.notify(context.Background(), "notifications/cancelled", map[string]any{"requestId": id, "reason": "timeout"})
		return nil, fmt.Errorf("%s 等待超时或被取消: %w", method, ctx.Err())
	case <-t.closed:
		if err, ok := t.readErr.Load().(error); ok && err != nil {
			return nil, fmt.Errorf("MCP 服务端连接断开: %w", err)
		}
		return nil, fmt.Errorf("MCP 服务端已退出%s", t.stderrHint())
	}
}

// setVersion 对 stdio 没有影响：版本只在 initialize 里声明一次。
func (t *stdioTransport) setVersion(string) {}

func (t *stdioTransport) setNotifyHook(fn func(string)) {
	if fn == nil {
		return
	}
	t.notifyHook.Store(&fn)
}

func (t *stdioTransport) notify(_ context.Context, method string, params any) error {
	return t.writeMessage(map[string]any{"jsonrpc": "2.0", "method": method, "params": paramsOrEmpty(params)})
}

// paramsOrEmpty 让 JSON 里出现 "params":{}，有的实现不接受缺省的 params。
func paramsOrEmpty(params any) any {
	if params == nil {
		return map[string]any{}
	}
	return params
}

func (t *stdioTransport) writeMessage(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if _, err := t.stdin.Write(data); err != nil {
		return fmt.Errorf("写入 MCP 服务端失败: %w%s", err, t.stderrHint())
	}
	return nil
}

func (t *stdioTransport) stderrHint() string {
	s := strings.TrimSpace(t.errBuf.String())
	if s == "" {
		return ""
	}
	return "\n服务端 stderr（末尾）:\n" + s
}

// close 按规范先关 stdin，等服务端退出；超时再杀整个进程树。
func (t *stdioTransport) close() error {
	_ = t.stdin.Close()
	select {
	case <-t.wait:
	case <-time.After(closeTimeout):
		// ConfigureCmd 设置的 Cancel 会终止整个进程树。
		t.cancel() // ConfigureCmd 设置的 Cancel 会终止整个进程树
		<-t.wait
	}
	return nil
}

// tailBuffer 只保留最后 limit 字节，给 stderr 这类只关心末尾的日志用。
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.limit {
		b.buf = append([]byte(nil), b.buf[len(b.buf)-b.limit:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
