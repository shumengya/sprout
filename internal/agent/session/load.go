package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/event"
)

// State 是从会话文件重放出来的状态：agent 的历史和摘要，以及给 TUI 重放用的事件流。
type State struct {
	Info    Info
	ID      string
	Path    string
	History []agent.Message
	Summary string
	Events  []event.Event
	Turns   int
	Skipped int // 损坏或写了一半的行数（比如进程被杀时最后一行）
}

// StripReasoning 清掉历史里的思维链及协议原始内容。换了模型/服务商后不应把别家的思维链回传给新模型。
func (s *State) StripReasoning() {
	for i := range s.History {
		s.History[i] = s.History[i].StripReasoning()
	}
}

// rawRec 是各类记录字段的并集，按 type 取用。
type rawRec struct {
	Type string `json:"type"`

	ID       string   `json:"id"`
	Cwd      string   `json:"cwd"`
	Provider string   `json:"provider"`
	Model    string   `json:"model"`
	Tools    []string `json:"tools"`
	Version  string   `json:"version"`

	Content    string        `json:"content"`
	Images     []imageRec    `json:"images"`
	Reasoning  string        `json:"reasoning"`
	ToolCalls  []toolCallRec `json:"tool_calls"`
	StopReason string        `json:"stop_reason"`
	Native     *event.Native `json:"native"`

	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	Command    string `json:"command"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	Truncated  bool   `json:"truncated"`
	TimedOut   bool   `json:"timed_out"`
	Cancelled  bool   `json:"cancelled"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error"`

	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	TotalTokens     int `json:"total_tokens"`
	ReasoningTokens int `json:"reasoning_tokens"` // 旧会话没有这个字段，读出来是 0

	Reason string `json:"reason"`

	Mode         string `json:"mode"`
	TokensBefore int    `json:"tokens_before"`
	TokensAfter  int    `json:"tokens_after"`
	Dropped      int    `json:"dropped"`
	Summary      string `json:"summary"`

	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// Load 读取会话文件并重放出状态。重放按 agent 运行时的同样规则还原历史：
// 并行工具的结果按调用顺序排列，被取消/失败的首次调用撤销用户消息，压缩按当初的方式重做。
// 损坏的行会被跳过并计数，而不是让整个会话无法恢复。
func Load(path string) (*State, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开会话文件失败: %w", err)
	}
	defer f.Close()

	r := &replayer{st: &State{Path: path}}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec rawRec
		if json.Unmarshal(line, &rec) != nil {
			r.st.Skipped++
			continue
		}
		r.handle(rec)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("读取会话文件失败: %w", err)
	}
	r.finish()
	r.st.History, r.st.Summary = r.history, r.summary
	return r.st, nil
}
