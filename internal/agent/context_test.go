package agent

import (
	"strings"
	"testing"
)

func TestEstimateTextCountsChineseHeavierThanAscii(t *testing.T) {
	// 规则细节见 tokens_test.go；这里只确认内核用的是同一套估算。
	if got := estimateText(strings.Repeat("字", 300)); got != EstimateTokens(strings.Repeat("字", 300)) || got <= 60 {
		t.Errorf("300 个汉字估算为 %d，应和 EstimateTokens 一致，且明显多于 300 个英文字母", got)
	}
	if estimateText("") != 0 {
		t.Error("空串应为 0")
	}
}

func TestEstimateMessageIncludesToolCallsAndReasoning(t *testing.T) {
	plain := estimateMessage(Message{Role: RoleAssistant, Content: "hi"})
	withCall := estimateMessage(Message{Role: RoleAssistant, Content: "hi",
		ToolCalls: []ToolCall{{Name: "bash", Arguments: `{"command":"ls -la"}`}}})
	withThink := estimateMessage(Message{Role: RoleAssistant, Content: "hi", Reasoning: strings.Repeat("想", 50)})
	if withCall <= plain || withThink < plain+30 {
		t.Errorf("tool call 和思维链都应计入: %d %d %d", plain, withCall, withThink)
	}
}

func TestContextBudget(t *testing.T) {
	if contextBudget(0, 1000) != 0 || contextBudget(-1, 1000) != 0 {
		t.Error("窗口未知时不应有预算（不做主动压缩）")
	}
	// 窗口 100000、输出上限 8000：预留 8000，剩余 92000 的 80%。
	if got := contextBudget(100000, 8000); got != 73600 {
		t.Errorf("预算 = %d，期望 73600", got)
	}
	// 输出上限大得离谱（比如 384000 配 100000 的窗口）时，预留封顶为窗口的 25%，不能把预算挤成负数。
	if got := contextBudget(100000, 384000); got != 60000 {
		t.Errorf("预算 = %d，期望 60000", got)
	}
	// 没配输出上限。
	if got := contextBudget(100000, 0); got != 80000 {
		t.Errorf("预算 = %d，期望 80000", got)
	}
}

func bigTool(id string, n int) Message {
	return Message{Role: RoleTool, ToolID: id, Content: strings.Repeat("x", n)}
}

func TestPruneOnlyTouchesOldLargeToolOutputs(t *testing.T) {
	h := []Message{
		{Role: RoleUser, Content: strings.Repeat("问", 2000)}, // 用户消息再长也不动
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "a"}}},
		bigTool("a", 5000), // 旧且大 → 裁
		bigTool("b", 100),  // 旧但小 → 不动
		{Role: RoleAssistant, Content: "ok"},
		{Role: RoleUser, Content: "再来"},
		bigTool("c", 5000), // 在最近 keep 条里 → 不动
	}
	n := PruneToolOutputs(h, 2)
	if n != 1 {
		t.Fatalf("应裁剪 1 条，实际 %d", n)
	}
	if len([]rune(h[2].Content)) >= 1000 || !strings.Contains(h[2].Content, "旧输出已裁剪") || !strings.Contains(h[2].Content, "5000") {
		t.Errorf("旧的大输出应被裁剪并注明原长度: %q", h[2].Content)
	}
	if h[3].Content != strings.Repeat("x", 100) || len([]rune(h[6].Content)) != 5000 || len([]rune(h[0].Content)) != 2000 {
		t.Error("小输出、最近的输出、用户消息都不应被改动")
	}
	// 配对关系不变：消息条数、角色、ToolID 都没变。
	if len(h) != 7 || h[2].Role != RoleTool || h[2].ToolID != "a" {
		t.Error("裁剪不应改变消息结构")
	}
}

func TestPruneIsIdempotent(t *testing.T) {
	h := []Message{bigTool("a", 5000), {Role: RoleUser, Content: "q"}, {Role: RoleUser, Content: "q"}}
	if PruneToolOutputs(h, 2) != 1 {
		t.Fatal("第一次应裁剪")
	}
	once := h[0].Content
	if PruneToolOutputs(h, 2) != 0 || h[0].Content != once {
		t.Fatal("重复裁剪不应再改动（也不能把标记当内容再裁一遍）")
	}
}

func TestPruneKeepsChineseIntact(t *testing.T) {
	h := []Message{{Role: RoleTool, Content: strings.Repeat("中", 3000)}, {Role: RoleUser}, {Role: RoleUser}}
	PruneToolOutputs(h, 2)
	if !strings.HasPrefix(h[0].Content, strings.Repeat("中", pruneKeepChars)+"\n") {
		t.Error("应按字符而不是字节截断，不能切坏汉字")
	}
}

func TestPruneWithShortHistoryIsNoop(t *testing.T) {
	h := []Message{bigTool("a", 5000)}
	if PruneToolOutputs(h, 6) != 0 || len(h[0].Content) != 5000 {
		t.Error("历史比 keep 还短时不应裁剪")
	}
}
