package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/shumengya/sprout/internal/rpc"
	"github.com/shumengya/sprout/internal/tui"
)

// sprout serve 里 MCP 服务端的输入请求不走终端界面，而是作为 JSON-RPC 请求发给客户端：
// mcp/elicit（填表）和 mcp/sample（确认采样）。协议见 docs/rpc.md。

// askField 是 mcp/elicit 请求里的一个字段，字段名即线上的 JSON 键。
type askField struct {
	Name     string   `json:"name"`
	Title    string   `json:"title,omitempty"`
	Desc     string   `json:"description,omitempty"`
	Kind     string   `json:"kind"`
	Choices  []string `json:"choices,omitempty"`
	Labels   []string `json:"labels,omitempty"`
	Required bool     `json:"required"`
	Default  any      `json:"default,omitempty"`
}

// rpcForward 返回把输入请求转给 RPC 客户端的函数。
//
// 客户端没在 initialize 里声明对应能力时直接拒绝：它不认识这类请求，
// 发过去只会让这一轮一直等不到回答。
func rpcForward(srv *rpc.Server) func(context.Context, *tui.Ask) (tui.AskAnswer, error) {
	return func(ctx context.Context, a *tui.Ask) (tui.AskAnswer, error) {
		method, capability := "mcp/elicit", rpc.CapElicitation
		params := map[string]any{"server": a.Server, "message": a.Message}
		if a.Kind == tui.AskKindSample {
			method, capability = "mcp/sample", rpc.CapSampling
		} else {
			fields := make([]askField, len(a.Fields))
			for i, f := range a.Fields {
				fields[i] = askField{f.Name, f.Title, f.Desc, f.Kind, f.Choices, f.Labels, f.Required, f.Default}
			}
			params["fields"] = fields
		}
		if !srv.ClientSupports(capability) {
			return tui.AskAnswer{}, fmt.Errorf("RPC 客户端没有在 initialize 里声明 %s 能力", capability)
		}

		raw, err := srv.Request(ctx, method, params)
		if err != nil {
			var rerr *rpc.Error
			if errors.As(err, &rerr) {
				return tui.AskAnswer{}, fmt.Errorf("客户端拒绝了请求：%s", rerr.Message)
			}
			return tui.AskAnswer{}, err
		}
		var res struct {
			Action string         `json:"action"`
			Values map[string]any `json:"values"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return tui.AskAnswer{}, fmt.Errorf("客户端的回答无法解析: %w", err)
		}
		switch res.Action {
		case tui.AskAccept, tui.AskDecline, tui.AskCancel:
		default:
			return tui.AskAnswer{}, fmt.Errorf("客户端的回答里 action=%q 不合法，需要 accept、decline 或 cancel", res.Action)
		}
		if res.Action != tui.AskAccept || a.Kind == tui.AskKindSample {
			return tui.AskAnswer{Action: res.Action}, nil
		}
		values, err := checkValues(a.Fields, res.Values)
		if err != nil {
			return tui.AskAnswer{}, err
		}
		return tui.AskAnswer{Action: tui.AskAccept, Values: values}, nil
	}
}

// checkValues 校验客户端填的值并转成 schema 要求的类型。TUI 里这件事由表单自己保证，
// 程序化的客户端没有这层，不查的话服务端会收到类型不对的数据。
func checkValues(fields []tui.AskField, values map[string]any) (map[string]any, error) {
	byName := make(map[string]tui.AskField, len(fields))
	for _, f := range fields {
		byName[f.Name] = f
	}
	for name := range values {
		if _, ok := byName[name]; !ok {
			return nil, fmt.Errorf("客户端回答了表单里没有的字段 %q", name)
		}
	}
	out := make(map[string]any, len(values))
	for _, f := range fields {
		v, ok := values[f.Name]
		if !ok || v == nil {
			if f.Required {
				return nil, fmt.Errorf("必填字段 %q 没有值", f.Name)
			}
			continue
		}
		switch f.Kind {
		case "string":
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("字段 %q 需要字符串", f.Name)
			}
			out[f.Name] = s
		case "boolean":
			b, ok := v.(bool)
			if !ok {
				return nil, fmt.Errorf("字段 %q 需要布尔值", f.Name)
			}
			out[f.Name] = b
		case "number":
			n, ok := v.(float64)
			if !ok {
				return nil, fmt.Errorf("字段 %q 需要数字", f.Name)
			}
			out[f.Name] = n
		case "integer":
			n, ok := v.(float64)
			if !ok || n != math.Trunc(n) || math.Abs(n) > 1<<53 {
				return nil, fmt.Errorf("字段 %q 需要整数", f.Name)
			}
			out[f.Name] = int64(n)
		case "enum":
			s, ok := v.(string)
			if !ok || !slices.Contains(f.Choices, s) {
				return nil, fmt.Errorf("字段 %q 的值必须是 %v 之一", f.Name, f.Choices)
			}
			out[f.Name] = s
		}
	}
	return out, nil
}
