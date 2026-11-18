package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/shumengya/sprout/internal/agent/event"
)

// turnStats 累计一轮里主对话的模型调用：一轮有工具往返时会调用多次模型，统计行是它们的合计。
// 子代理的用量不算在内：它们是另一段对话，花费照常计入状态栏。
type turnStats struct {
	// start 是本轮开始的时刻。恢复会话重放时为零：记录里没有当初的耗时，统计行只写 token。
	start time.Time
	// ttft 是从提交到第一个输出（思维链或正文）的时间，gotFirst 表示已经记下。
	ttft     time.Duration
	gotFirst bool
	calls    int
	// input 是没命中缓存的输入，和状态栏的“输入”口径一致；cacheRead + cacheWrite + input 才是全部输入。
	input, output, cacheRead, cacheWrite int
	// genTokens、genDur 是能算速度的那些调用（流式、报告了输出 token）的输出与生成时间，速度按它们的合计算。
	genTokens int
	genDur    time.Duration
}

// markFirst 在一次调用收到首个输出时调用；只有本轮第一次会被记下。
func (s *turnStats) markFirst(at time.Time) {
	if s.gotFirst || s.start.IsZero() {
		return
	}
	s.ttft, s.gotFirst = max(at.Sub(s.start), 0), true
}

func (s *turnStats) add(e event.Usage) {
	s.calls++
	s.input += max(e.InputTokens-e.CacheReadTokens-e.CacheWriteTokens, 0)
	s.output += e.OutputTokens
	s.cacheRead += e.CacheReadTokens
	s.cacheWrite += e.CacheWriteTokens
}

// line 渲染统计行，如「耗时 12s · 首字 1.2s · 45.6 token/s · 输入 2.1k · 输出 1.2k · 缓存 10k/0 · 合计 13k」。
// 没有任何用量（出错或取消在模型回应之前）时返回空串，不显示统计行；缺的数据项直接省略，不写 0。
func (s turnStats) line(now time.Time) string {
	if s.calls == 0 {
		return ""
	}
	var parts []string
	if !s.start.IsZero() {
		parts = append(parts, "耗时 "+fmtSeconds(now.Sub(s.start)))
	}
	if s.gotFirst {
		parts = append(parts, "首字 "+fmtSeconds(s.ttft))
	}
	if s.genTokens > 0 && s.genDur > 0 {
		parts = append(parts, fmt.Sprintf("%.1f token/s", float64(s.genTokens)/s.genDur.Seconds()))
	}
	parts = append(parts, "输入 "+fmtTokens(s.input), "输出 "+fmtTokens(s.output))
	if s.cacheRead+s.cacheWrite > 0 {
		parts = append(parts, "缓存 "+fmtTokens(s.cacheRead)+"/"+fmtTokens(s.cacheWrite))
	}
	parts = append(parts, "合计 "+fmtTokens(s.input+s.cacheRead+s.cacheWrite+s.output))
	return strings.Join(parts, " · ")
}
