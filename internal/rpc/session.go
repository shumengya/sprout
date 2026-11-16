package rpc

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/event"
)

func (s *Server) unavailable() *Error {
	if info := s.b.Info(); !info.Available {
		return errorf(CodeUnavailable, "%s", info.Disabled)
	}
	return nil
}

func (s *Server) listSessions(params json.RawMessage) (any, func(), *Error) {
	var p struct {
		All bool `json:"all"`
	}
	if err := decodeParams(params, &p); err != nil {
		return nil, nil, err
	}
	list, err := s.b.ListSessions(p.All)
	if err != nil {
		return nil, nil, errorf(CodeSession, "列出会话失败: %v", err)
	}
	if list == nil {
		list = []SessionInfo{} // 没有会话时给空数组，客户端不必区分 null
	}
	return map[string]any{"sessions": list}, nil, nil
}

func (s *Server) resumeSession(params json.RawMessage) (any, func(), *Error) {
	var p struct {
		Selector string `json:"selector"`
	}
	if err := decodeParams(params, &p); err != nil {
		return nil, nil, err
	}
	if s.busy() {
		return nil, nil, errBusy()
	}
	if err := s.unavailable(); err != nil {
		return nil, nil, err
	}
	r, err := s.b.ResumeSession(p.Selector)
	if err != nil {
		return nil, nil, errorf(CodeSession, "%v", err)
	}
	return restoredResult(r), nil, nil
}

// branchSession 把当前会话的前 turns 轮（省略或 0 为全部）复制成新会话并切过去，结果格式与 session/resume 相同。
func (s *Server) branchSession(params json.RawMessage) (any, func(), *Error) {
	var p struct {
		Turns int `json:"turns"`
	}
	if err := decodeParams(params, &p); err != nil {
		return nil, nil, err
	}
	if p.Turns < 0 {
		return nil, nil, errorf(CodeInvalidParams, "turns 不能为负数")
	}
	if s.busy() {
		return nil, nil, errBusy()
	}
	if err := s.unavailable(); err != nil {
		return nil, nil, err
	}
	r, err := s.b.BranchSession(p.Turns)
	if err != nil {
		return nil, nil, errorf(CodeSession, "%v", err)
	}
	return restoredResult(r), nil, nil
}

func restoredResult(r Restored) map[string]any {
	// 回放的事件不带 turn：turn 是本进程里 turn/start 的编号，历史轮次没有对应的编号，
	// 硬编一个会和之后实时的编号撞上。客户端按 turn.started 分轮即可。
	evs := make([]map[string]any, 0, len(r.Events))
	for _, e := range r.Events {
		if w := wireEvent(0, e); w != nil {
			delete(w, "turn")
			evs = append(evs, w)
		}
	}
	return map[string]any{"id": r.ID, "path": r.Path, "notice": r.Notice, "turns": r.Turns, "events": evs}
}

func (s *Server) renameSession(params json.RawMessage) (any, func(), *Error) {
	var p struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := decodeParams(params, &p); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(p.Path) == "" {
		return nil, nil, errorf(CodeInvalidParams, "path 不能为空")
	}
	if err := s.b.RenameSession(p.Path, p.Name); err != nil {
		return nil, nil, errorf(CodeSession, "%v", err)
	}
	return struct{}{}, nil, nil
}

func (s *Server) deleteSession(params json.RawMessage) (any, func(), *Error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := decodeParams(params, &p); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(p.Path) == "" {
		return nil, nil, errorf(CodeInvalidParams, "path 不能为空")
	}
	if err := s.b.DeleteSession(p.Path); err != nil {
		return nil, nil, errorf(CodeSession, "%v", err)
	}
	return struct{}{}, nil, nil
}

// compact 在后台生成摘要，结束后才写响应：压缩要调一次模型，可能要几十秒，期间读循环要能收到 turn/cancel。
// 压缩不是一轮对话，不推送 event；结果直接放在响应里。
func (s *Server) compact(id json.RawMessage, params json.RawMessage) (any, func(), *Error) {
	var p struct {
		Focus string `json:"focus"`
	}
	if err := decodeParams(params, &p); err != nil {
		return nil, nil, err
	}
	if s.busy() {
		return nil, nil, errBusy()
	}
	if err := s.unavailable(); err != nil {
		return nil, nil, err
	}
	notify := len(id) == 0
	ctx, finish := s.begin()
	run := func() {
		var got *event.Compacted
		sink := event.SinkFunc(func(e event.Event) {
			if c, ok := e.(event.Compacted); ok {
				got = &c
			}
		})
		err := s.b.Compact(ctx, strings.TrimSpace(p.Focus), sink)
		var result any
		var rerr *Error
		switch {
		case err == nil && got != nil:
			result = map[string]any{
				"tokensBefore": got.TokensBefore, "tokensAfter": got.TokensAfter,
				"dropped": got.Dropped, "summary": got.Summary,
			}
		case ctx.Err() != nil:
			rerr = errorf(CodeCancelled, "压缩已取消，历史保持原样")
		case errors.Is(err, agent.ErrNothingToCompact):
			rerr = errorf(CodeCompactFailed, "%v", err)
		case err != nil:
			rerr = errorf(CodeCompactFailed, "生成摘要失败，历史保持原样: %v", err)
		default:
			rerr = errorf(CodeInternal, "压缩结束但没有结果")
		}
		finish(func() {
			if !notify {
				s.reply(id, result, rerr)
			}
		})
	}
	return pending{}, func() { go run() }, nil
}
