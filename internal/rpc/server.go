package rpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/event"
	"github.com/shumengya/sprout/internal/media"
)

// ProtocolVersion 是本协议的版本号，initialize 返回给客户端。字段只增不改时不升级。
const ProtocolVersion = 1

// Backend 是服务端眼中的运行时：当前 agent、会话记录器和配置都在它背后。
// 除 Run、Compact 外的方法都在读循环里串行调用，实现不需要自己加锁防并发；
// 会替换 agent 或历史的方法（Prepare、NewSession、ResumeSession、SwitchModel）只在没有一轮进行时调用。
type Backend interface {
	Info() Info
	// Prepare 在空闲时调用（开始一轮之前、initialize 时），把后台连好的 MCP 工具等接进 agent，
	// 返回要告知客户端的提示。
	Prepare() []string
	Run(ctx context.Context, in agent.Input, sink event.Sink) error
	NewSession() error
	// ListSessions 列出当前工作目录（all 为 true 时是全部目录）的会话，最近使用的在前。
	ListSessions(all bool) ([]SessionInfo, error)
	// ResumeSession 按选择器恢复会话：latest、ListSessions(false) 里的序号、ID 前缀或会话文件路径。
	ResumeSession(selector string) (Restored, error)
	RenameSession(path, name string) error
	DeleteSession(path string) error
	// Compact 把全部历史总结成摘要。和 Run 一样在后台调用，可被取消。
	Compact(ctx context.Context, focus string, sink event.Sink) error
	ListModels() []ModelInfo
	// SwitchModel 切换到 ref（provider/model），对话历史带过去，返回给用户看的说明。
	// 模型不存在时返回 NotFoundError；失败时保持原模型。
	SwitchModel(ref string) (string, error)
	Efforts() EffortList
	// SetEffort 切换思考档位，下一次请求生效；档位不存在或模型不支持时返回 NotFoundError。
	// 可以在一轮进行中调用。
	SetEffort(name string) (string, error)
}

// SessionInfo 是 session/list 里的一项。会话按 Path 定位：重命名、删除都用它。
type SessionInfo struct {
	ID       string    `json:"id"`
	Path     string    `json:"path"`
	Prompt   string    `json:"prompt"` // 第一条提问
	Name     string    `json:"name"`   // 用户起的名字，没起过为空
	Cwd      string    `json:"cwd"`
	Provider string    `json:"provider"`
	Model    string    `json:"model"`
	Updated  time.Time `json:"updated"`
	Turns    int       `json:"turns"`
	Current  bool      `json:"current"` // 是当前正在写的会话
}

// Restored 是恢复会话的结果。Events 是按会话记录重放出的事件，客户端用它画出历史对话。
type Restored struct {
	ID     string
	Path   string
	Notice string
	Turns  int
	Events []event.Event
}

// Info 是 initialize 的结果。
type Info struct {
	ProtocolVersion int      `json:"protocolVersion"`
	Version         string   `json:"version"`
	Model           string   `json:"model"`
	Provider        string   `json:"provider"`
	Cwd             string   `json:"cwd"`
	DataDir         string   `json:"dataDir"`
	Tools           []string `json:"tools"`
	Vision          bool     `json:"vision"`
	Effort          string   `json:"effort"` // 当前思考档位；模型没有声明 effort 时为空
	// Available 为 false 表示当前不能对话（缺 API Key 等），原因在 Disabled 里；turn/start 会返回 -32002。
	Available bool     `json:"available"`
	Disabled  string   `json:"disabled,omitempty"`
	Notices   []string `json:"notices"`
}

// Server 在一对读写流上服务一个客户端。同一时刻最多一轮在进行，进行中再 turn/start 返回 busy。
type Server struct {
	b   Backend
	out io.Writer
	log *slog.Logger

	wmu sync.Mutex // 一条消息一行，事件通知和响应来自不同 goroutine，写入必须整行互斥

	// 服务端向客户端发请求的状态（见 request.go）：待回的请求、客户端声明的能力。
	rmu       sync.Mutex
	nextReq   int
	reqs      map[string]chan incoming
	caps      map[string]bool
	closed    chan struct{} // Serve 返回时关闭，让还在等客户端回答的请求结束
	closeOnce sync.Once

	mu     sync.Mutex
	turn   int
	cancel context.CancelFunc
	done   chan struct{} // 非 nil 表示有一轮在进行；Run 返回后关闭
}

// NewServer 创建服务端。log 为 nil 时不记日志。
func NewServer(b Backend, out io.Writer, log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Server{b: b, out: out, log: log, closed: make(chan struct{})}
}

// Serve 逐行读取请求，直到输入结束、收到 shutdown 或 ctx 取消。
// 返回前取消进行中的一轮，最多等 wait 让它收尾（会话记录要写完 turn.finished）。
// 只有 ctx 取消时返回非 nil。
func (s *Server) Serve(ctx context.Context, in io.Reader, wait time.Duration) error {
	lines := make(chan []byte)
	quit := make(chan struct{})
	defer close(quit)
	go func() {
		defer close(lines)
		// 不用 bufio.Scanner：它有单行长度上限，超长的一行会让整个连接读不下去。
		r := bufio.NewReader(in)
		for {
			line, err := r.ReadBytes('\n')
			if len(bytes.TrimSpace(line)) > 0 {
				select {
				case lines <- line:
				case <-quit:
					return
				}
			}
			if err != nil {
				if err != io.EOF {
					s.log.Warn("读取请求失败", "error", err.Error())
				}
				return
			}
		}
	}()

	defer s.stop(wait)
	// 先于 stop 执行：进行中的一轮可能正卡在等客户端回答，得先放它走，stop 才等得到它结束。
	defer s.closeOnce.Do(func() { close(s.closed) })
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case line, ok := <-lines:
			if !ok || s.handle(line) {
				return nil
			}
		}
	}
}

// stop 取消进行中的一轮并等它结束，最多等 wait。
func (s *Server) stop(wait time.Duration) {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if done == nil {
		return
	}
	cancel()
	select {
	case <-done:
	case <-time.After(wait):
		s.log.Warn("退出时这一轮没能在限定时间内结束", "wait", wait.String())
	}
}

// handle 处理一行输入，返回 true 表示客户端要求退出。
func (s *Server) handle(line []byte) bool {
	line = bytes.TrimSpace(line)
	if line[0] == '[' {
		s.reply(nullID, nil, errorf(CodeInvalidRequest, "不支持批量请求，请逐条发送"))
		return false
	}
	var m incoming
	if err := json.Unmarshal(line, &m); err != nil {
		s.reply(nullID, nil, errorf(CodeParseError, "不是合法的 JSON: %v", err))
		return false
	}
	notify := len(m.ID) == 0
	if m.Method == "" && (m.Result != nil || m.Error != nil) {
		s.deliver(m) // 客户端对 Request 的回答；对不上的直接丢弃
		return false
	}
	if m.JSONRPC != "2.0" || m.Method == "" || (!notify && !validID(m.ID)) {
		if !notify {
			id := m.ID
			if !validID(id) {
				id = nullID
			}
			s.reply(id, nil, errorf(CodeInvalidRequest, "不是合法的 JSON-RPC 2.0 请求（需要 jsonrpc: \"2.0\" 和 method）"))
		}
		return false
	}

	s.log.Debug("rpc 请求", "method", m.Method, "notify", notify)
	result, after, rerr := s.call(m.ID, m.Method, m.Params)
	if _, later := result.(pending); !notify && !later {
		s.reply(m.ID, result, rerr)
	}
	// 响应先写出去再开始这一轮，客户端拿到 {turn} 之后才会收到这一轮的事件。
	if after != nil {
		after()
	}
	return m.Method == "shutdown" && rerr == nil
}

// pending 作为 call 的结果时表示响应由后台操作结束后自己写（session/compact）。
type pending struct{}

func (s *Server) call(id json.RawMessage, method string, params json.RawMessage) (any, func(), *Error) {
	switch method {
	case "session/list":
		return s.listSessions(params)
	case "session/resume":
		return s.resumeSession(params)
	case "session/rename":
		return s.renameSession(params)
	case "session/delete":
		return s.deleteSession(params)
	case "session/compact":
		return s.compact(id, params)
	case "model/list":
		return s.listModels()
	case "model/switch":
		return s.switchModel(params)
	case "effort/list":
		return s.listEfforts()
	case "effort/set":
		return s.setEffort(params)
	case "initialize":
		s.setCaps(params)
		var prepared []string
		if !s.busy() {
			prepared = s.b.Prepare()
		}
		info := s.b.Info() // Prepare 之后再取：接入 MCP 后工具列表会变
		info.Notices = append(info.Notices, prepared...)
		info.ProtocolVersion = ProtocolVersion
		return info, nil, nil
	case "turn/start":
		return s.startTurn(params)
	case "turn/cancel":
		s.mu.Lock()
		cancel := s.cancel
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return map[string]bool{"cancelled": cancel != nil}, nil, nil
	case "session/new":
		if s.busy() {
			return nil, nil, errBusy()
		}
		if !s.b.Info().Available {
			return nil, nil, errorf(CodeUnavailable, "%s", s.b.Info().Disabled)
		}
		if err := s.b.NewSession(); err != nil {
			return nil, nil, errorf(CodeInternal, "新建会话失败: %v", err)
		}
		return struct{}{}, nil, nil
	case "shutdown":
		return struct{}{}, nil, nil
	}
	return nil, nil, errorf(CodeMethodNotFound, "未知方法 %q", method)
}

func errBusy() *Error {
	return errorf(CodeBusy, "已有一轮对话或压缩在进行，等它结束再发，或先 turn/cancel")
}

// begin 标记忙碌，返回这次后台操作的 ctx 和收尾函数。收尾函数先清掉忙碌状态，再调用 last
// 告诉客户端“结束了”（客户端一收到就可能发下一个请求），最后才让 stop 不再等待，保证最后一条消息写出去。
func (s *Server) begin() (context.Context, func(last func())) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.mu.Lock()
	s.cancel, s.done = cancel, done
	s.mu.Unlock()
	return ctx, func(last func()) {
		s.mu.Lock()
		s.cancel, s.done = nil, nil
		s.mu.Unlock()
		cancel()
		last()
		close(done)
	}
}

func (s *Server) busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done != nil
}

type startParams struct {
	Text   string `json:"text"`
	Images []struct {
		Path string `json:"path"`
	} `json:"images"`
}

func (s *Server) startTurn(params json.RawMessage) (any, func(), *Error) {
	var p startParams
	if err := decodeParams(params, &p); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(p.Text) == "" && len(p.Images) == 0 {
		return nil, nil, errorf(CodeInvalidParams, "text 与 images 不能都为空")
	}
	if s.busy() {
		return nil, nil, errBusy()
	}
	if info := s.b.Info(); !info.Available {
		return nil, nil, errorf(CodeUnavailable, "%s", info.Disabled)
	}
	in := agent.Input{Text: p.Text}
	for _, im := range p.Images {
		if strings.TrimSpace(im.Path) == "" {
			return nil, nil, errorf(CodeInvalidParams, "images[].path 不能为空")
		}
		img, _, err := media.LoadImage(im.Path)
		if err != nil {
			return nil, nil, errorf(CodeInvalidParams, "%v", err)
		}
		in.Images = append(in.Images, img)
	}
	for _, n := range s.b.Prepare() {
		s.notify("notice", map[string]string{"message": n})
	}

	s.mu.Lock()
	s.turn++
	n := s.turn
	s.mu.Unlock()
	ctx, finish := s.begin()

	run := func() {
		// turn.finished 压到 Run 返回、忙碌状态清掉之后再发：客户端一收到它就可能发下一个请求，
		// 这时还没结束的 Run 会被当成 busy；会话记录器也要在它之后才写完这一轮。
		var finished map[string]any
		sink := event.SinkFunc(func(e event.Event) {
			w := wireEvent(n, e)
			switch {
			case w == nil:
			case e.Kind() == event.KindTurnFinished:
				finished = w
			default:
				s.notify("event", w)
			}
		})
		if err := s.b.Run(ctx, in, sink); err != nil {
			s.log.Info("本轮以错误结束", "turn", n, "error", err.Error())
		}
		finish(func() {
			if finished != nil {
				s.notify("event", finished)
			}
		})
	}
	return map[string]int{"turn": n}, func() { go run() }, nil
}

func (s *Server) reply(id json.RawMessage, result any, err *Error) {
	if err == nil && result == nil {
		result = struct{}{} // 成功响应必须带 result 成员
	}
	s.write(response{JSONRPC: "2.0", ID: id, Result: result, Error: err})
}

func (s *Server) notify(method string, params any) {
	s.write(notification{JSONRPC: "2.0", Method: method, Params: params})
}

func (s *Server) write(v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // 命令输出里的 < > & 原样保留，客户端日志更好读
	if err := enc.Encode(v); err != nil {
		s.log.Error("编码 rpc 消息失败", "error", err.Error())
		return
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if _, err := s.out.Write(buf.Bytes()); err != nil {
		s.log.Warn("写出 rpc 消息失败", "error", err.Error())
	}
}
