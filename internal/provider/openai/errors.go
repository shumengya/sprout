package openai

import "github.com/shumengya/sprout/internal/provider/transport"

// 错误归一化与工具函数在 transport 里，与 Anthropic / Gemini 共用；这里保留短名字。
var (
	parseAPIError    = transport.ParseAPIError
	parseRetryAfter  = transport.ParseRetryAfter
	decodeErrorValue = transport.DecodeErrorValue
	clipText         = transport.ClipText
	newCallID        = transport.NewCallID
	streamError      = transport.StreamError
	interrupted      = transport.Interrupted
)
