package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shumengya/sprout/internal/agent/event"
)

// RetryPolicy 描述模型调用失败后的重试策略。只有 ProviderError.Retryable 的错误才会重试。
type RetryPolicy struct {
	MaxRetries int           // 最多重试几次；0 表示不重试
	BaseDelay  time.Duration // 第一次重试前的等待，之后每次翻倍
	MaxDelay   time.Duration // 单次等待的上限
}

// DefaultRetry 是 Config.Retry 为 nil 时使用的策略：最多 7 次，等待 1s、2s、4s、8s、16s、32s、64s。
var DefaultRetry = RetryPolicy{MaxRetries: 7, BaseDelay: time.Second, MaxDelay: 64 * time.Second}

// 服务端用 Retry-After 要求等待超过这个时间，就不等了，直接把错误交给用户。
const maxServerWait = 64 * time.Second

// next 判断第 attempt 次（从 1 起）重试是否应该进行，并给出等待时间。
func (p RetryPolicy) next(attempt int, err error) (time.Duration, bool) {
	var pe *ProviderError
	if !errors.As(err, &pe) || !pe.Retryable || attempt > p.MaxRetries {
		return 0, false
	}
	if pe.RetryAfter > 0 {
		return pe.RetryAfter, pe.RetryAfter <= maxServerWait
	}
	d := p.BaseDelay << (attempt - 1)
	if p.MaxDelay > 0 && (d > p.MaxDelay || d <= 0) {
		d = p.MaxDelay
	}
	// 不加抖动：单个终端客户端不存在“一群客户端同时重试”的问题，固定的 1s、2s、4s… 用户看得懂。
	return d, true
}

// sleepCtx 等待 d，ctx 被取消时立即返回 ctx.Err()，所以退避期间按 Ctrl+C 能马上取消。
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	tm := time.NewTimer(d)
	defer tm.Stop()
	select {
	case <-tm.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// retrying 执行一次模型调用，对可重试的错误做退避重试。
//
// call 在向 surface 输出任何内容之前必须先调用 started：一旦输出过，这次调用就不能再重试，
// 否则已经显示（或已经写进 stdout）的半截答案会被重复输出。
func (t *turn) retrying(call func(started func()) (Response, error)) (Response, error) {
	for attempt := 1; ; attempt++ {
		emitted := false
		resp, err := call(func() { emitted = true })
		if err == nil || t.ctx.Err() != nil || emitted {
			return resp, err
		}
		delay, ok := t.a.retry.next(attempt, err)
		if !ok {
			// 退避到上限仍失败：把次数写进错误里，用户才知道是重试过以后才放弃的，
			// 而不是一次就报错。用 %w 包住原错误，上层仍能 errors.As 拿到 ProviderError。
			if attempt > 1 {
				var pe *ProviderError
				if errors.As(err, &pe) && pe.Retryable {
					return resp, fmt.Errorf("已重试 %d 次仍失败：%w", attempt-1, err)
				}
			}
			return resp, err
		}
		reason, _, _ := strings.Cut(err.Error(), "\n")
		t.out.Emit(event.Retrying{Attempt: attempt, Max: t.a.retry.MaxRetries, Delay: delay, Reason: clip(reason, 120)})
		t.a.log.Warn("retrying model call", "attempt", attempt, "delay", delay, "error", reason)
		if serr := sleepCtx(t.ctx, delay); serr != nil {
			return Response{}, serr
		}
	}
}
