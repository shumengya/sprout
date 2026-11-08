package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/shumengya/sprout/internal/agent/event"
	"github.com/shumengya/sprout/internal/agent/tool"
)

// 日志里命令的最大长度；完整命令在会话 JSONL 里。
const maxLoggedCommand = 200

const cancelledToolReply = "未执行：用户取消了本轮对话。"

// runTools 串行执行一批 tool call，并把结果按顺序追加到历史。
// 只在本轮被取消时返回 error；此时尚未执行的调用也会补上“未执行”结果，
// 保证每个 assistant tool call 在历史里都有对应的 tool 消息，否则下一次请求会被服务端拒绝。
func (t *turn) runTools(calls []ToolCall) error {
	for i, call := range calls {
		if t.ctx.Err() != nil {
			t.skip(calls[i:])
			return t.cancelled()
		}
		t.a.history = append(t.a.history, t.runTool(call))
	}
	if t.ctx.Err() != nil { // 最后一个工具执行期间被取消
		return t.cancelled()
	}
	return nil
}

// skip 为没来得及执行的调用补上“已取消”的结果。
func (t *turn) skip(calls []ToolCall) {
	for _, c := range calls {
		t.out.Emit(event.ToolFinished{ID: c.ID, Name: c.Name, Cancelled: true, Err: cancelledToolReply})
		t.a.history = append(t.a.history, Message{Role: RoleTool, ToolID: c.ID, Content: cancelledToolReply})
	}
}

// runTool 执行单个调用。工具的任何失败（未知工具、参数无效、进程起不来、被取消）
// 都转成 tool 消息返回给模型，让它看到失败原因，不会终止 agent。
func (t *turn) runTool(call ToolCall) Message {
	t.out.Emit(event.ToolRequested{Call: event.ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments}})
	fin := event.ToolFinished{ID: call.ID, Name: call.Name}
	var content string

	tl, ok := t.lookup(call.Name)
	if !ok {
		fin.Err = fmt.Sprintf("未知工具 %q，可用工具: %s", call.Name, t.toolNames())
		content = "错误: " + fin.Err
	} else {
		start := time.Now()
		res, err := tl.Run(t.ctx, json.RawMessage(call.Arguments))
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
		}
		t.a.log.Info("tool", "name", call.Name, "command", clip(res.Command, maxLoggedCommand),
			"exit_code", res.ExitCode, "timed_out", res.TimedOut, "duration", fin.Duration, "error", fin.Err)
	}

	t.out.Emit(fin)
	return Message{Role: RoleTool, ToolID: call.ID, Content: content}
}

func fillFinished(fin *event.ToolFinished, res tool.Result, wall time.Duration) {
	fin.Command, fin.Stdout, fin.Stderr = res.Command, res.Stdout, res.Stderr
	fin.ExitCode, fin.Truncated, fin.TimedOut = res.ExitCode, res.Truncated, res.TimedOut
	fin.Duration = res.Duration
	if fin.Duration == 0 {
		fin.Duration = wall
	}
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
