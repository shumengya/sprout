package rpc

import (
	"encoding/json"
	"errors"
	"strings"
)

// ModelInfo 是 model/list 里的一项。
type ModelInfo struct {
	Ref           string `json:"ref"` // provider/model，model/switch 用它
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	Protocol      string `json:"protocol"`
	ContextWindow int    `json:"contextWindow"`
	MaxOutput     int    `json:"maxOutput"`
	Tools         bool   `json:"tools"`
	Vision        bool   `json:"vision"`
	Current       bool   `json:"current"`
}

// EffortList 是 effort/list 的结果。模型没有声明 effort 时 Levels 为空，Unsupported 说明怎么配置。
type EffortList struct {
	Current     string        `json:"current"`
	Levels      []EffortLevel `json:"levels"`
	Unsupported string        `json:"unsupported,omitempty"`
}

// EffortLevel 是一个思考档位。Detail 用该协议的真实字段名描述会发送什么参数。
type EffortLevel struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`
}

// NotFoundError 由 Backend 返回，表示要切换的模型或档位不存在，服务端据此返回 -32004 而不是一般的失败。
type NotFoundError struct{ Err error }

func (e NotFoundError) Error() string { return e.Err.Error() }
func (e NotFoundError) Unwrap() error { return e.Err }

func (s *Server) listModels() (any, func(), *Error) {
	list := s.b.ListModels()
	if list == nil {
		list = []ModelInfo{}
	}
	return map[string]any{"models": list}, nil, nil
}

// switchModel 会重建 agent，必须空闲；历史原样带到新模型。
func (s *Server) switchModel(params json.RawMessage) (any, func(), *Error) {
	var p struct {
		Ref string `json:"ref"`
	}
	if err := decodeParams(params, &p); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(p.Ref) == "" {
		return nil, nil, errorf(CodeInvalidParams, "ref 不能为空")
	}
	if s.busy() {
		return nil, nil, errBusy()
	}
	if err := s.unavailable(); err != nil {
		return nil, nil, err
	}
	notice, err := s.b.SwitchModel(strings.TrimSpace(p.Ref))
	if err != nil {
		return nil, nil, notFoundOr(err, "切换模型失败，仍在使用原模型: ")
	}
	info := s.b.Info()
	return map[string]any{"model": info.Model, "effort": info.Effort, "notice": notice}, nil, nil
}

func (s *Server) listEfforts() (any, func(), *Error) {
	l := s.b.Efforts()
	if l.Levels == nil {
		l.Levels = []EffortLevel{}
	}
	return l, nil, nil
}

// setEffort 不检查忙碌：只改思考参数，不重建 agent，进行中的一轮从下一次请求起生效（和 TUI 一致）。
func (s *Server) setEffort(params json.RawMessage) (any, func(), *Error) {
	var p struct {
		Name string `json:"name"`
	}
	if err := decodeParams(params, &p); err != nil {
		return nil, nil, err
	}
	name := strings.ToLower(strings.TrimSpace(p.Name))
	if name == "" {
		return nil, nil, errorf(CodeInvalidParams, "name 不能为空")
	}
	if err := s.unavailable(); err != nil {
		return nil, nil, err
	}
	notice, err := s.b.SetEffort(name)
	if err != nil {
		return nil, nil, notFoundOr(err, "")
	}
	return map[string]any{"effort": s.b.Efforts().Current, "notice": notice}, nil, nil
}

func notFoundOr(err error, prefix string) *Error {
	var nf NotFoundError
	if errors.As(err, &nf) {
		return errorf(CodeNotFound, "%v", err)
	}
	return errorf(CodeInternal, "%s%v", prefix, err)
}
