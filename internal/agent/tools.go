package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	"github.com/shumengya/sprout/internal/agent/event"
	"github.com/shumengya/sprout/internal/agent/tool"
)

// 日志里命令的最大长度；完整命令在会话 JSONL 里。
const maxLoggedCommand = 200

const cancelledToolReply = "未执行：用户取消了本轮对话。"

// execTool 执行单个调用，返回 ToolFinished 事件和回给模型的文本。工具的任何失败（未知工具、参数无效、
// 进程起不来、被取消、甚至 panic）都转成文本返回给模型，让它看到失败原因，不会终止 agent。
//
// 它不发事件、不碰历史，所以可以在多个 goroutine 里并发调用；事件和历史由 runTools 在主 goroutine 里统一处理。
// ctx 是本轮的 ctx 加上这次调用的 tool.Call。
func (t *turn) execTool(ctx context.Context, call ToolCall) (fin event.ToolFinished, content string) {
	fin = event.ToolFinished{ID: call.ID, Name: call.Name}
	defer func() {
		if r := recover(); r != nil {
			// 工具实现的 bug 不能拖垮整个进程，也不该作废整轮：当作一次工具失败告诉模型。
			fin.Err = fmt.Sprintf("内部错误: %v", r)
			content = "工具执行失败: " + fin.Err
			t.a.log.Error("tool panic", "name", call.Name, "panic", r, "stack", string(debug.Stack()))
		}
	}()

	tl, ok := t.lookup(call.Name)
	if !ok {
		fin.Err = fmt.Sprintf("未知工具 %q，可用工具: %s", call.Name, t.toolNames())
		content = "错误: " + fin.Err
	} else {
		start := time.Now()
		res, err := tl.Run(ctx, json.RawMessage(call.Arguments))
		fillFinished(&fin, res, time.Since(start))
		switch {
		case err != nil && t.ctx.Err() != nil:
			fin.Cancelled, fin.Err = true, "已取消"
			content = "工具执行被用户取消。"
			if res.Command != "" {
				content += "以下是取消前已收集的输出：\n" + res.ForModel()
			}
		case err != nil:
			fin.Err = err.Error()
			content = "工具执行失败: " + err.Error()
		default:
			content = res.ForModel()
			fin.Images = resultImages(res.Images)
		}
		t.a.log.Info("tool", "name", call.Name, "command", clip(res.Command, maxLoggedCommand),
			"exit_code", res.ExitCode, "timed_out", res.TimedOut, "duration", fin.Duration, "error", fin.Err)
	}

	return fin, content
}

func fillFinished(fin *event.ToolFinished, res tool.Result, wall time.Duration) {
	fin.Command, fin.Stdout, fin.Stderr = res.Command, res.Stdout, res.Stderr
	fin.ExitCode, fin.Truncated, fin.TimedOut = res.ExitCode, res.Truncated, res.TimedOut
	fin.Duration = res.Duration
	if fin.Duration == 0 {
		fin.Duration = wall
	}
}

// resultImages 把工具结果里的图片转成事件里的图片描述；只在工具正常结束时调用，失败或取消时不附图。
func resultImages(imgs []tool.Image) []event.ImageInfo {
	var out []event.ImageInfo
	for _, img := range imgs {
		out = append(out, event.ImageInfo{
			MediaType: img.MediaType, Bytes: base64.StdEncoding.DecodedLen(len(img.Data)), Data: img.Data,
		})
	}
	return out
}

// toolResultMessage 构造工具结果消息：文本在前，图片按原顺序跟在后面。
// 会话重放（session.replayer.tool）按同样的顺序还原，两处要一起改。
func toolResultMessage(callID, content string, images []event.ImageInfo) Message {
	m := toolMessage(callID, content)
	for _, img := range images {
		m.Content = append(m.Content, ImageBlock(img.MediaType, img.Data))
	}
	return m
}

func (t *turn) lookup(name string) (tool.Tool, bool) {
	if t.a.cfg.Tools == nil {
		return nil, false
	}
	return t.a.cfg.Tools.Get(name)
}

func (t *turn) toolNames() string {
	if t.a.cfg.Tools == nil || len(t.a.cfg.Tools.Names()) == 0 {
		return "（无）"
	}
	return strings.Join(t.a.cfg.Tools.Names(), "、")
}

// clip 按字符截断，避免日志里出现被切坏的 UTF-8。
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
