package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
)

// 服务端向客户端发起的请求（MCP 服务端要用户填表或确认采样时用）。
//
// 只有客户端在 initialize 里声明了对应能力才会发；没声明时调用方应直接按拒绝处理，
// 否则一个不认识这类请求的客户端会让这一轮永远等下去。

// Client 能力名，对应 initialize 参数 capabilities 里的字段。
const (
	CapElicitation = "elicitation"
	CapSampling    = "sampling"
)

// ErrClientGone 表示等待客户端回答期间连接结束了。
var ErrClientGone = errors.New("rpc 连接已结束，客户端没有回答")

// ClientSupports 报告客户端是否在 initialize 里声明了某项能力。
func (s *Server) ClientSupports(capability string) bool {
	s.rmu.Lock()
	defer s.rmu.Unlock()
	return s.caps[capability]
}

// setCaps 解析 initialize 的 capabilities。缺省或字段无法解析时视为都不支持。
func (s *Server) setCaps(params json.RawMessage) {
	var p struct {
		Capabilities map[string]bool `json:"capabilities"`
	}
	caps := map[string]bool{}
	if err := decodeParams(params, &p); err == nil {
		for k, v := range p.Capabilities {
			caps[k] = v
		}
	}
	s.rmu.Lock()
	s.caps = caps
	s.rmu.Unlock()
}

// Request 向客户端发一条请求并等响应。客户端回错误时返回 *Error；
// ctx 取消或连接结束时返回对应的错误。
func (s *Server) Request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s.rmu.Lock()
	s.nextReq++
	id := "srv-" + strconv.Itoa(s.nextReq)
	ch := make(chan incoming, 1)
	if s.reqs == nil {
		s.reqs = map[string]chan incoming{}
	}
	s.reqs[id] = ch
	s.rmu.Unlock()
	defer func() {
		s.rmu.Lock()
		delete(s.reqs, id)
		s.rmu.Unlock()
	}()

	rawID, _ := json.Marshal(id)
	s.write(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  any             `json:"params"`
	}{"2.0", rawID, method, params})

	select {
	case m := <-ch:
		if len(m.Error) > 0 && string(m.Error) != "null" {
			var e Error
			if err := json.Unmarshal(m.Error, &e); err != nil {
				return nil, errorf(CodeInternal, "客户端返回了无法解析的错误: %s", m.Error)
			}
			return nil, &e
		}
		return m.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.closed:
		return nil, ErrClientGone
	}
}

// deliver 把客户端发来的响应交给等它的 Request。找不到对应请求的响应（已取消、id 对不上）直接丢弃。
func (s *Server) deliver(m incoming) {
	var id string
	if err := json.Unmarshal(m.ID, &id); err != nil {
		return
	}
	s.rmu.Lock()
	ch := s.reqs[id]
	s.rmu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- m:
	default: // 同一个 id 回了两次，只认第一次
	}
}
