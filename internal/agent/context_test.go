package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent/event"
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
	plain := estimateMessage(Message{Role: RoleAssistant, Content: []Block{TextBlock("hi")}})
	withCall := estimateMessage(Message{Role: RoleAssistant, Content: []Block{TextBlock("hi")},
		ToolCalls: []ToolCall{{Name: "bash", Arguments: `{"command":"ls -la"}`}}})
	withThink := estimateMessage(Message{Role: RoleAssistant, Content: []Block{TextBlock("hi")}, Reasoning: strings.Repeat("想", 50)})
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
	return Message{Role: RoleTool, ToolID: id, Content: []Block{TextBlock(strings.Repeat("x", n))}}
}

func TestPruneOnlyTouchesOldLargeToolOutputs(t *testing.T) {
	h := []Message{
		{Role: RoleUser, Content: []Block{TextBlock(strings.Repeat("问", 2000))}}, // 用户消息再长也不动
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "a"}}},
		bigTool("a", 5000), // 旧且大 → 裁
		bigTool("b", 100),  // 旧但小 → 不动
		{Role: RoleAssistant, Content: []Block{TextBlock("ok")}},
		{Role: RoleUser, Content: []Block{TextBlock("再来")}},
		bigTool("c", 5000), // 在最近 keep 条里 → 不动
	}
	n := PruneToolOutputs(h, 2, 0)
	if n != 1 {
		t.Fatalf("应裁剪 1 条，实际 %d", n)
	}
	if len([]rune(h[2].Text())) >= 1000 || !strings.Contains(h[2].Text(), "旧输出已裁剪") || !strings.Contains(h[2].Text(), "5000") {
		t.Errorf("旧的大输出应被裁剪并注明原长度: %q", h[2].Text())
	}
	if h[3].Text() != strings.Repeat("x", 100) || len([]rune(h[6].Text())) != 5000 || len([]rune(h[0].Text())) != 2000 {
		t.Error("小输出、最近的输出、用户消息都不应被改动")
	}
	// 配对关系不变：消息条数、角色、ToolID 都没变。
	if len(h) != 7 || h[2].Role != RoleTool || h[2].ToolID != "a" {
		t.Error("裁剪不应改变消息结构")
	}
}

func TestPruneIsIdempotent(t *testing.T) {
	h := []Message{bigTool("a", 5000), {Role: RoleUser, Content: []Block{TextBlock("q")}}, {Role: RoleUser, Content: []Block{TextBlock("q")}}}
	if PruneToolOutputs(h, 2, 0) != 1 {
		t.Fatal("第一次应裁剪")
	}
	once := h[0].Text()
	if PruneToolOutputs(h, 2, 0) != 0 || h[0].Text() != once {
		t.Fatal("重复裁剪不应再改动（也不能把标记当内容再裁一遍）")
	}
}

func TestPruneKeepsChineseIntact(t *testing.T) {
	h := []Message{{Role: RoleTool, Content: []Block{TextBlock(strings.Repeat("中", 3000))}}, {Role: RoleUser}, {Role: RoleUser}}
	PruneToolOutputs(h, 2, 0)
	if !strings.HasPrefix(h[0].Text(), strings.Repeat("中", pruneKeepChars)+"\n") {
		t.Error("应按字符而不是字节截断，不能切坏汉字")
	}
}

func TestPruneWithShortHistoryIsNoop(t *testing.T) {
	h := []Message{bigTool("a", 5000)}
	if PruneToolOutputs(h, 6, 0) != 0 || len(h[0].Text()) != 5000 {
		t.Error("历史比 keep 还短时不应裁剪")
	}
}

// prunedFlags 返回每条 tool 消息是否已被裁剪，顺序同历史。
func prunedFlags(h []Message) []bool {
	var out []bool
	for _, m := range h {
		if m.Role == RoleTool {
			out = append(out, isPruned(m.Text()))
		}
	}
	return out
}

func threeBigTools() []Message {
	return []Message{bigTool("a", 3000), bigTool("b", 3000), bigTool("c", 3000), {Role: RoleUser}, {Role: RoleUser}}
}

func TestPruneLimitCutsOnlyTheOldest(t *testing.T) {
	h := threeBigTools()
	if n := PruneToolOutputs(h, 2, 2); n != 2 {
		t.Fatalf("limit=2 应裁 2 条，实际 %d", n)
	}
	if got := prunedFlags(h); !got[0] || !got[1] || got[2] {
		t.Fatalf("应只裁最旧的两条: %v", got)
	}
	// 再来一次不限条数：只剩没裁过的那一条。
	if n := PruneToolOutputs(h, 2, 0); n != 1 || !prunedFlags(h)[2] {
		t.Fatalf("limit<=0 应裁掉剩下的全部: n=%d %v", n, prunedFlags(h))
	}
}

// 旧版本一次裁光所有候选，记录里的 Dropped 就是候选总数。重放时用它当 limit，结果必须与不限条数一致。
func TestPruneLimitEqualToCandidatesMatchesUnlimited(t *testing.T) {
	a, b := threeBigTools(), threeBigTools()
	n := PruneToolOutputs(a, 2, 0)
	PruneToolOutputs(b, 2, n)
	for i := range a {
		if a[i].Text() != b[i].Text() {
			t.Fatalf("第 %d 条不同:\n%q\n%q", i, a[i].Text(), b[i].Text())
		}
	}
}

func TestPruneTowardStopsOnceEnoughIsSaved(t *testing.T) {
	h := threeBigTools()
	perMsg := estimateMessage(h[0]) - estimateMessage(Message{Role: RoleTool,
		Content: []Block{TextBlock(strings.Repeat("x", pruneKeepChars) + "\n" + fmt.Sprintf(pruneMarker, 3000))}})
	if perMsg <= 0 {
		t.Fatalf("前提不成立：裁剪应省下 token，每条 %d", perMsg)
	}

	// 只需要省一点点：裁最旧的一条就够，剩下两条原样保留。
	n, saved, _ := pruneToward(h, 2, 1, 0)
	got := prunedFlags(h)
	if n != 1 || saved != perMsg || !got[0] || got[1] || got[2] {
		t.Fatalf("need=1 应只裁最旧一条: n=%d saved=%d %v", n, saved, got)
	}
	// 需要的比能省的还多：裁完所有候选就停，不会无限循环或越界。
	n, saved, _ = pruneToward(h, 2, 1<<30, 0)
	if n != 2 || saved != 2*perMsg {
		t.Fatalf("应裁光剩下的两条: n=%d saved=%d（每条 %d）", n, saved, perMsg)
	}
	// 没有候选了：什么都不做。
	if n, saved, _ = pruneToward(h, 2, 100, 0); n != 0 || saved != 0 {
		t.Fatalf("没有候选时应为空操作: n=%d saved=%d", n, saved)
	}
}

func TestPruneTowardSplitsSavingsAtTheCalibrationPoint(t *testing.T) {
	// 校准点之内的那部分省下的 token 要单独报告：服务端的真实用量只对应校准点之前的消息。
	h := threeBigTools()
	perMsg := estimateMessage(h[0]) - estimateMessage(Message{Role: RoleTool,
		Content: []Block{TextBlock(strings.Repeat("x", pruneKeepChars) + "\n" + fmt.Sprintf(pruneMarker, 3000))}})
	n, saved, inCalib := pruneToward(h, 2, 1<<30, 2) // 校准点在第 2 条之后：a、b 在内，c 在外
	if n != 3 || saved != 3*perMsg || inCalib != 2*perMsg {
		t.Fatalf("n=%d saved=%d inCalib=%d，期望 3 / %d / %d", n, saved, inCalib, 3*perMsg, 2*perMsg)
	}
}

func TestCompactDeductsPrunedTokensFromCalibration(t *testing.T) {
	// 裁剪后不能把校准复位成纯估算：纯估算整体偏高，会让紧接着的下一步又判定超限。
	h := []Message{{Role: RoleUser, Content: []Block{TextBlock("问")}}, bigTool("a", 6000), bigTool("b", 6000)}
	h = append(h, textTurns(3, 5)...)
	p := &scripted{}
	a := compactAgent(t, p, 2000, h) // 预算约 1500
	a.calibTokens, a.calibLen = 1900, len(h)
	var c collector
	changed := (&turn{a: a, ctx: context.Background(), out: &c}).compact(false)
	evs := compactedEvents(&c)
	if !changed || len(evs) != 1 || evs[0].Mode != event.CompactPrune {
		t.Fatalf("应只发生一次裁剪: changed=%v %+v", changed, evs)
	}
	if a.calibLen != len(h) || a.calibTokens >= 1900 || a.calibTokens <= 0 {
		t.Fatalf("应保留校准点并扣掉被裁的部分: calibTokens=%d calibLen=%d", a.calibTokens, a.calibLen)
	}
	if evs[0].TokensBefore != 1900 || evs[0].TokensAfter != a.calibTokens {
		t.Errorf("事件里的前后估算应基于校准值: %+v，校准 %d", evs[0], a.calibTokens)
	}
}
