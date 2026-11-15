package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shumengya/sprout/internal/agent/event"
	"github.com/shumengya/sprout/internal/agent/tool"
)

var fast = &RetryPolicy{MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond}

func transient() error { return &ProviderError{Status: 503, Retryable: true, Message: "overloaded"} }

func TestRetrySucceedsAfterTransientFailures(t *testing.T) {
	p := &scripted{steps: []scriptStep{{err: transient()}, {err: transient()}, say("终于好了")}}
	a := newAgentCfg(t, Config{Provider: p, Retry: fast})
	var c collector

	if err := a.Run(context.Background(), Input{Text: "q"}, &c); err != nil {
		t.Fatalf("重试成功后不应返回错误: %v", err)
	}
	if len(p.reqs) != 3 {
		t.Fatalf("应调用 3 次，实际 %d", len(p.reqs))
	}
	want := []event.Kind{event.KindTurnStarted, event.KindRetrying, event.KindRetrying,
		event.KindMessageDelta, event.KindMessageCompleted, event.KindTurnFinished}
	if !slices.Equal(c.kinds(), want) {
		t.Fatalf("事件序列 = %v，期望 %v", c.kinds(), want)
	}
	r1, r2 := c.events[1].(event.Retrying), c.events[2].(event.Retrying)
	if r1.Attempt != 1 || r2.Attempt != 2 || r1.Max != 3 || r1.Reason == "" {
		t.Errorf("重试事件 = %+v / %+v", r1, r2)
	}
	if c.has(event.KindError) || c.finishedReason(t) != event.ReasonStop {
		t.Error("最终成功就不应有 Error 事件")
	}
	// 每次重试请求的内容必须一致，历史里也不能多出东西。
	for i := 1; i < 3; i++ {
		if len(p.reqs[i].Messages) != len(p.reqs[0].Messages) {
			t.Errorf("第 %d 次请求的历史长度变了", i+1)
		}
	}
}

func TestRetryGivesUpAfterMaxRetries(t *testing.T) {
	p := &scripted{steps: []scriptStep{{err: transient()}, {err: transient()}, {err: transient()}, {err: transient()}, say("不该到这")}}
	a := newAgentCfg(t, Config{Provider: p, Retry: fast})
	var c collector

	err := a.Run(context.Background(), Input{Text: "q"}, &c)
	var pe *ProviderError
	if !errors.As(err, &pe) || pe.Status != 503 {
		t.Fatalf("耗尽重试后应返回原错误，得到 %v", err)
	}
	if len(p.reqs) != 4 { // 1 次原始 + 3 次重试
		t.Fatalf("应调用 4 次，实际 %d", len(p.reqs))
	}
	if c.finishedReason(t) != event.ReasonError || !c.has(event.KindError) {
		t.Errorf("事件 = %v", c.kinds())
	}
	// 失败的那轮保留在历史里，并补一条带原因的 assistant 消息。
	if h := a.History(); len(h) != 2 || !strings.Contains(h[1].Text(), "中断") {
		t.Errorf("失败后应保留本轮并写明原因: %+v", h)
	}
}

func TestNonRetryableErrorIsNotRetried(t *testing.T) {
	p := &scripted{steps: []scriptStep{{err: &ProviderError{Status: 401}}, say("不该到这")}}
	a := newAgentCfg(t, Config{Provider: p, Retry: fast})
	var c collector
	_ = a.Run(context.Background(), Input{Text: "q"}, &c)
	if len(p.reqs) != 1 || c.has(event.KindRetrying) {
		t.Fatalf("401 不应重试: 调用 %d 次", len(p.reqs))
	}
}

func TestZeroPolicyDisablesRetry(t *testing.T) {
	p := &scripted{steps: []scriptStep{{err: transient()}, say("不该到这")}}
	a := newAgentCfg(t, Config{Provider: p, Retry: &RetryPolicy{}})
	_ = a.Run(context.Background(), Input{Text: "q"}, &collector{})
	if len(p.reqs) != 1 {
		t.Fatalf("空策略应关闭重试，调用了 %d 次", len(p.reqs))
	}
}

func TestNilPolicyUsesDefault(t *testing.T) {
	a := newAgentCfg(t, Config{Provider: &scripted{}})
	if a.retry != DefaultRetry {
		t.Fatalf("nil 应使用默认策略: %+v", a.retry)
	}
}

func TestNoRetryOnceOutputWasEmitted(t *testing.T) {
	// 已经向界面输出了半截答案，再重试会重复输出（CLI 的 stdout 更是收不回），所以必须直接报错。
	p := &scripted{steps: []scriptStep{
		{chunks: []string{"半截"}, err: &ProviderError{Retryable: true, Message: "连接中断"}},
		say("不该到这"),
	}}
	a := newAgentCfg(t, Config{Provider: p, Retry: fast})
	var c collector
	if err := a.Run(context.Background(), Input{Text: "q"}, &c); err == nil {
		t.Fatal("应返回错误")
	}
	if len(p.reqs) != 1 || c.has(event.KindRetrying) {
		t.Fatalf("输出过内容后不应重试: 调用 %d 次", len(p.reqs))
	}
}

func TestRetryAfterHeaderIsHonoured(t *testing.T) {
	p := RetryPolicy{MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
	d, ok := p.next(1, &ProviderError{Retryable: true, RetryAfter: 7 * time.Second})
	if !ok || d != 7*time.Second {
		t.Fatalf("应听服务端的: %v %v", d, ok)
	}
	// 服务端要求等很久：不等了，直接把错误交给用户。
	if _, ok := p.next(1, &ProviderError{Retryable: true, RetryAfter: time.Hour}); ok {
		t.Fatal("要求等待超过上限时不应重试")
	}
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	p := RetryPolicy{MaxRetries: 10, BaseDelay: 100 * time.Millisecond, MaxDelay: 800 * time.Millisecond}
	err := &ProviderError{Retryable: true}
	var prevMid time.Duration
	for attempt := 1; attempt <= 6; attempt++ {
		d, ok := p.next(attempt, err)
		if !ok {
			t.Fatalf("第 %d 次应允许重试", attempt)
		}
		mid := min(p.BaseDelay<<(attempt-1), p.MaxDelay)
		if d != mid {
			t.Errorf("第 %d 次等待 %v，期望 %v", attempt, d, mid)
		}
		if mid < prevMid {
			t.Errorf("退避时间不应缩短")
		}
		prevMid = mid
	}
	if _, ok := p.next(11, err); ok {
		t.Error("超过 MaxRetries 不应再重试")
	}
}

func TestBackoffOverflowIsSafe(t *testing.T) {
	p := RetryPolicy{MaxRetries: 100, BaseDelay: time.Hour, MaxDelay: time.Minute}
	if d, ok := p.next(80, &ProviderError{Retryable: true}); !ok || d != time.Minute {
		t.Fatalf("移位溢出时应退回上限: %v %v", d, ok)
	}
}

func TestCancelDuringBackoffReturnsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &scripted{steps: []scriptStep{{err: transient()}, say("不该到这")}}
	slow := &RetryPolicy{MaxRetries: 3, BaseDelay: time.Minute, MaxDelay: time.Minute}
	a := newAgentCfg(t, Config{Provider: p, Retry: slow})
	var c collector
	time.AfterFunc(80*time.Millisecond, cancel)

	start := time.Now()
	err := a.Run(ctx, Input{Text: "q"}, &c)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("应返回 context.Canceled，得到 %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("退避等待期间取消应立即生效")
	}
	if c.finishedReason(t) != event.ReasonCancelled || c.has(event.KindError) {
		t.Errorf("取消后事件 %v", c.kinds())
	}
	if h := a.History(); len(h) != 2 || !strings.Contains(h[1].Text(), "用户取消") {
		t.Errorf("取消后应保留本轮并写明原因: %+v", h)
	}
}

func TestRetryInLaterStepKeepsEarlierToolResults(t *testing.T) {
	// 工具执行之后的那次模型调用失败再重试：历史必须保持“assistant tool_calls + tool 结果”原样。
	p := &scripted{steps: []scriptStep{
		callTools(call("c1", "bash", `{}`)),
		{err: transient()},
		say("好了"),
	}}
	a := newAgentCfg(t, Config{Provider: p, Retry: fast, Tools: registry(t, okTool("bash", tool.Result{}))})
	if err := a.Run(context.Background(), Input{Text: "q"}, &collector{}); err != nil {
		t.Fatal(err)
	}
	if len(p.reqs) != 3 || len(p.reqs[1].Messages) != 3 || len(p.reqs[2].Messages) != 3 {
		t.Fatalf("重试请求的历史应与失败那次一致: %d 次调用", len(p.reqs))
	}
}

// 默认策略：1s、2s、4s、8s、16s、32s、64s 共 7 次，第 8 次不再重试。
func TestDefaultRetrySchedule(t *testing.T) {
	err := &ProviderError{Retryable: true}
	var got []time.Duration
	for attempt := 1; ; attempt++ {
		d, ok := DefaultRetry.next(attempt, err)
		if !ok {
			break
		}
		got = append(got, d)
	}
	want := []time.Duration{1, 2, 4, 8, 16, 32, 64}
	for i := range want {
		want[i] *= time.Second
	}
	if !slices.Equal(got, want) {
		t.Fatalf("默认退避 = %v，期望 %v", got, want)
	}
}

// 重试耗尽后的错误要说明重试过几次，同时仍能取到原始的 ProviderError。
func TestRetryExhaustedErrorSaysHowManyTimes(t *testing.T) {
	p := &scripted{steps: []scriptStep{{err: transient()}, {err: transient()}, {err: transient()}, {err: transient()}}}
	a := newAgentCfg(t, Config{Provider: p, Retry: fast})
	var c collector
	err := a.Run(context.Background(), Input{Text: "q"}, &c)
	var pe *ProviderError
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), "已重试 3 次仍失败") || !strings.Contains(err.Error(), "overloaded") {
		t.Fatalf("错误 = %v", err)
	}
}
