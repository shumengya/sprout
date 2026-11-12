package session

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/event"
)

const interruptedReply = "未执行：会话在这个工具完成前被中断。"

// replayer 把会话记录重放成 agent 的历史。规则与 agent 运行时保持一致，否则恢复出来的历史
// 和当初模型看到的不同。
type replayer struct {
	st      *State
	history []agent.Message
	summary string
	base    int // 当前这一轮开始前的历史长度（不含本轮的用户消息）
	inTurn  bool

	pending  []agent.ToolCall         // 最近一条 assistant 发起、还没落入历史的 tool call
	results  map[string]agent.Message // 已收到的工具结果，按 call id
	panicked bool                     // 本轮因 panic 失败：整轮作废
}

// flush 把 pending 的工具结果按调用顺序写进历史。并行执行时记录里是按完成顺序出现的，
// 而 tool 消息必须和 assistant 的 tool_calls 一一对应、顺序一致。进程中途被杀留下的未完成调用，
// 补上“未执行”，否则下一次请求会因为 tool_call 没有对应结果被服务端拒绝。
func (r *replayer) flush() {
	for _, c := range r.pending {
		m, ok := r.results[c.ID]
		if !ok {
			m = agent.NewTextMessage(agent.RoleTool, interruptedReply)
			m.ToolID = c.ID
			r.st.Events = append(r.st.Events, event.ToolFinished{
				ID: c.ID, Name: c.Name, Cancelled: true, Err: interruptedReply, Content: interruptedReply,
			})
		}
		r.history = append(r.history, m)
	}
	r.pending, r.results = nil, nil
}

func (r *replayer) emit(e event.Event) { r.st.Events = append(r.st.Events, e) }

func (r *replayer) handle(rec rawRec) {
	switch rec.Type {
	case "session":
		r.st.ID, r.st.Info = rec.ID, Info{Provider: rec.Provider, Model: rec.Model, Cwd: rec.Cwd, Tools: rec.Tools, Version: rec.Version}
	case "user":
		r.flush()
		r.base, r.inTurn, r.panicked = len(r.history), true, false
		// 与 Input.Message 的规则一致：纯图片提问不补空文本块（部分协议会拒绝空 text block）。
		var content []agent.Block
		if rec.Content != "" || len(rec.Images) == 0 {
			content = append(content, agent.TextBlock(rec.Content))
		}
		var images []event.ImageInfo
		for _, img := range rec.Images {
			blk, err := loadImageFile(r.st.Path, img)
			if err != nil {
				// 图片文件被删或损坏：保留一条文字说明，别让整轮无法恢复。
				content = append(content, agent.TextBlock("（图片无法恢复："+img.File+"）"))
				continue
			}
			content = append(content, blk)
			name := img.Name
			if name == "" { // 旧记录没有原始文件名
				name = filepath.Base(img.File)
			}
			images = append(images, event.ImageInfo{
				Name: name, MediaType: img.MediaType, Bytes: img.Bytes, Source: img.Source,
			})
		}
		r.history = append(r.history, agent.Message{Role: agent.RoleUser, Content: content})
		r.emit(event.TurnStarted{Prompt: rec.Content, Images: images})
	case "assistant":
		r.flush()
		msg := agent.Message{Role: agent.RoleAssistant, Reasoning: rec.Reasoning, Native: rec.Native}
		if rec.Content != "" { // 空文本与运行时一致：不产生文本块
			msg.Content = []agent.Block{agent.TextBlock(rec.Content)}
		}
		var calls []event.ToolCall
		for _, c := range rec.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, agent.ToolCall{ID: c.ID, Name: c.Name, Arguments: c.Arguments})
			calls = append(calls, event.ToolCall{ID: c.ID, Name: c.Name, Arguments: c.Arguments})
		}
		r.history = append(r.history, msg)
		r.pending, r.results = msg.ToolCalls, map[string]agent.Message{}
		r.emit(event.MessageCompleted{Content: rec.Content, Reasoning: rec.Reasoning, ToolCalls: calls, StopReason: rec.StopReason,
			ReasoningTokensEst: agent.EstimateTokens(rec.Reasoning)})
	case "tool":
		r.tool(rec)
	case "interrupted":
		r.flush()
		msg := agent.Message{Role: agent.RoleAssistant, Reasoning: rec.Reasoning}
		if rec.Content != "" {
			msg.Content = []agent.Block{agent.TextBlock(rec.Content)}
		}
		r.history = append(r.history, msg)
		r.pending, r.results = nil, nil
		r.emit(event.Interrupted{Reason: rec.Reason, Content: rec.Content, Reasoning: rec.Reasoning})
	case "usage":
		r.emit(event.Usage{InputTokens: rec.InputTokens, OutputTokens: rec.OutputTokens, TotalTokens: rec.TotalTokens,
			ReasoningTokens: rec.ReasoningTokens})
	case "compact":
		r.compact(rec)
	case "error":
		// agent 在内部错误（panic）时会把历史还原到本轮开始前，重放要做同样的事。
		r.panicked = strings.HasPrefix(rec.Message, "内部错误")
		r.emit(event.Error{Message: rec.Message, Retryable: rec.Retryable})
	case "turn":
		r.endTurn(rec.Reason)
	}
	// retry / resume 以及未知类型：只用于审计，恢复时忽略。
}

func (r *replayer) tool(rec rawRec) {
	var call *agent.ToolCall
	for i := range r.pending {
		if r.pending[i].ID == rec.ToolCallID {
			call = &r.pending[i]
		}
	}
	if call == nil {
		return // 找不到对应的 tool call（记录不完整），忽略，免得历史里出现孤立的 tool 消息
	}
	content := rec.Content
	if content == "" { // 旧版本的会话没有保存回给模型的文本，退而求其次
		content = legacyContent(rec)
	}
	tm := agent.NewTextMessage(agent.RoleTool, content)
	tm.ToolID = call.ID
	r.results[call.ID] = tm
	r.emit(event.ToolRequested{Call: event.ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments}})
	r.emit(event.ToolFinished{
		ID: call.ID, Name: rec.Name, Command: rec.Command, Stdout: rec.Stdout, Stderr: rec.Stderr,
		ExitCode: rec.ExitCode, Truncated: rec.Truncated, TimedOut: rec.TimedOut, Cancelled: rec.Cancelled,
		Duration: time.Duration(rec.DurationMS) * time.Millisecond, Err: rec.Error, Content: content,
	})
}

// loadImageFile 读取 user 记录引用的图片文件并转成图片块。路径相对会话文件所在目录。
func loadImageFile(sessionPath string, img imageRec) (agent.Block, error) {
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(sessionPath), filepath.FromSlash(img.File)))
	if err != nil {
		return agent.Block{}, fmt.Errorf("读取图片失败: %w", err)
	}
	return agent.ImageBlock(img.MediaType, base64.StdEncoding.EncodeToString(raw)), nil
}

// legacyContent 为没有保存 content 的旧记录重建一个等价的文本。
func legacyContent(rec rawRec) string {
	switch {
	case rec.Cancelled:
		return "工具执行被用户取消。"
	case rec.Error != "":
		return "工具执行失败: " + rec.Error
	}
	return rec.Stdout + rec.Stderr
}

func (r *replayer) compact(rec rawRec) {
	r.flush()
	if rec.Error != "" {
		r.emit(event.Compacted{Mode: rec.Mode, TokensBefore: rec.TokensBefore, TokensAfter: rec.TokensAfter, Err: rec.Error})
		return
	}
	switch rec.Mode {
	case event.CompactSummary:
		cut := min(rec.Dropped, len(r.history))
		r.history = append([]agent.Message(nil), r.history[cut:]...)
		agent.DropNative(r.history) // 与运行时一致：压缩后不再回传原始思考块
		r.summary = rec.Summary
		r.base = max(r.base-cut, 0)
	case event.CompactPrune:
		agent.PruneToolOutputs(r.history, agent.PruneKeepRecent)
	}
	r.emit(event.Compacted{
		Mode: rec.Mode, TokensBefore: rec.TokensBefore, TokensAfter: rec.TokensAfter,
		Dropped: rec.Dropped, Summary: rec.Summary,
	})
}

func (r *replayer) endTurn(reason string) {
	r.flush()
	switch {
	case r.panicked:
		r.history = r.history[:min(r.base, len(r.history))]
	case len(r.history) == r.base+1 && (reason == event.ReasonCancelled || reason == event.ReasonError):
		// 旧版本会话：运行时会撤销首次调用的用户消息，重放保持一致。
		// 新版本在结束时补一条 interrupted 记录，历史长度是 base+2，不会走到这里。
		r.history = r.history[:r.base]
	}
	r.inTurn, r.panicked = false, false
	r.st.Turns++
	r.emit(event.TurnFinished{Reason: reason})
}

// finish 处理文件结束时的收尾：最后一轮没有 turn 记录说明进程是被杀掉的。
// 这种情况下没有 interrupted 记录可还原，历史只留用户消息即可——下一轮请求前它仍然合法。
func (r *replayer) finish() {
	if !r.inTurn {
		r.flush()
		return
	}
	r.flush()
	if len(r.history) == r.base+1 { // 只有用户消息，模型还没回应：当作被取消
		r.history = r.history[:r.base]
	}
	r.emit(event.TurnFinished{Reason: event.ReasonCancelled})
	r.inTurn = false
}
