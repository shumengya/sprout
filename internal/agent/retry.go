package agent

import (
	"context"
	"errors"
	"math/rand/v2"
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

// DefaultRetry 是 Config.Retry 为 nil 时使用的策略：最多 3 次，等待约 1s、2s、4s。
var DefaultRetry = RetryPolicy{MaxRetries: 3, BaseDelay: time.Second, MaxDelay: 16 * time.Second}

// 服务端用 Retry-After 要求等待超过这个时间，就不等了，直接把错误交给用户。
const maxServerWait = 30 * time.Second

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
	// ±25% 抖动，避免多个客户端同时重试。
	return d + time.Duration((rand.Float64()-0.5)*0.5*float64(d)), true
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
