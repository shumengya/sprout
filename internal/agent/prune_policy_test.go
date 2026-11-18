package agent

import (
	"fmt"
	"strings"
	"testing"
)

// toolTurn 生成一次工具调用与它的结果：assistant 发起调用，tool 消息带 body。
func toolTurn(id, name, args, body string) []Message {
	return []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: id, Name: name, Arguments: args}}},
		{Role: RoleTool, ToolID: id, Content: []Block{TextBlock(body)}},
	}
}

// numberedLines 生成 n 行形如「行 i: ……」的文本，每行 width 个字符左右。
func numberedLines(n, width int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "行 %d: %s\n", i, strings.Repeat("x", width))
	}
	return b.String()
}

func TestPruneKeepsHeadAndTailOfShellOutput(t *testing.T) {
	// 命令输出的报错和测试汇总在最后：只留开头会把最关键的部分裁掉。
	body := "command: go test ./...\nexit_code: 1\n" + numberedLines(100, 40) + "FAIL: TestSomething\n"
	h := toolTurn("c1", "bash", `{"command":"go test ./..."}`, body)
	if n := PruneToolOutputs(h, 0, 0, PruneVersion); n != 1 {
		t.Fatalf("应裁剪 1 条，实际 %d", n)
	}
	got := h[1].Text()
	if !strings.HasPrefix(got, "command: go test ./...\nexit_code: 1\n") || !strings.HasSuffix(got, "FAIL: TestSomething\n") {
		t.Fatalf("应保留开头的命令与退出码和结尾的报错:\n%s", got)
	}
	if !isPruned(got) || !strings.Contains(got, fmt.Sprintf("原文共 %d 字符", len([]rune(body)))) {
		t.Errorf("中间应有裁剪标记并注明原长度:\n%s", got)
	}
	// 结尾从整行开始，不会以半截行开头。
	_, tail, _ := strings.Cut(got, "字符]\n")
	if !strings.HasPrefix(tail, "行 ") {
		t.Errorf("结尾应从整行开始: %.40q", tail)
	}
	if len([]rune(got)) >= len([]rune(body))/2 {
		t.Errorf("裁剪后应明显变短: %d → %d", len([]rune(body)), len([]rune(got)))
	}
}

func TestPruneReadOutputKeepsOnlyTheHeadAndSaysItCanBeReRead(t *testing.T) {
	body := "command: read C:\\work\\big.go\n" + numberedLines(200, 40)
	h := toolTurn("r1", "read", `{"path":"C:\\work\\big.go"}`, body)
	PruneToolOutputs(h, 0, 0, PruneVersion)
	got := h[1].Text()
	head := policyFor(PruneVersion, "read").head
	if !strings.HasPrefix(got, string([]rune(body)[:head])+"\n") || !strings.HasSuffix(got, "需要时用 read 重新读取]") {
		t.Fatalf("read 的结果应只留开头，并提示可以重新读取:\n%s", got)
	}
	if !isPruned(got) {
		t.Error("带提示的标记也要被认作已裁剪")
	}
}

func TestPrunePolicyDependsOnTheTool(t *testing.T) {
	body := numberedLines(300, 40)
	var h []Message
	h = append(h, toolTurn("a1", "agent", `{"prompt":"调查"}`, body)...)
	h = append(h, toolTurn("m1", "mcp__docs__search", `{}`, body)...)
	h = append(h, toolTurn("x1", "my_tool", `{}`, body)...) // tools.jsonc 里的外部命令工具
	if n := PruneToolOutputs(h, 0, 0, PruneVersion); n != 3 {
		t.Fatalf("应裁剪 3 条，实际 %d", n)
	}
	sub, mcp, ext := h[1].Text(), h[3].Text(), h[5].Text()
	if len([]rune(sub)) <= len([]rune(ext)) {
		t.Errorf("子代理的报告应比普通命令输出保留得多: %d vs %d", len([]rune(sub)), len([]rune(ext)))
	}
	if !strings.HasSuffix(sub, "\n") || !strings.HasSuffix(ext, "\n") {
		t.Error("子代理与外部命令的结果应保留结尾")
	}
	if !strings.HasSuffix(mcp, "字符]") {
		t.Errorf("MCP 工具的结果应只留开头: %.80q", mcp[len(mcp)-80:])
	}
}

func TestPruneSkipsMediumOutputsThatWouldBarelyShrink(t *testing.T) {
	// 原文只比保留部分长一点时不裁：省不了多少，却要改动历史、让前缀缓存失效。
	p := policyFor(PruneVersion, "bash")
	h := toolTurn("c1", "bash", `{}`, strings.Repeat("x", p.minChars()))
	if n := PruneToolOutputs(h, 0, 0, PruneVersion); n != 0 {
		t.Errorf("刚好到阈值不应裁剪，实际裁了 %d 条", n)
	}
}

func TestPruneVersionOneKeepsTheOldUniformRule(t *testing.T) {
	// 旧会话的 prune 记录没有版本，恢复时必须按当年的规则裁，模型看到的历史才和当初一样。
	body := numberedLines(100, 40)
	for _, version := range []int{0, 1} {
		h := toolTurn("c1", "bash", `{}`, body)
		if n := PruneToolOutputs(h, 0, 0, version); n != 1 {
			t.Fatalf("版本 %d: 应裁剪 1 条，实际 %d", version, n)
		}
		want := string([]rune(body)[:200]) + "\n" + fmt.Sprintf("…[旧输出已裁剪以节省上下文，原文共 %d 字符]", len([]rune(body)))
		if got := h[1].Text(); got != want {
			t.Errorf("版本 %d 应与旧规则逐字一致:\n%q\n%q", version, got, want)
		}
	}
	// 版本 1 的阈值也不变：超过 600 字符就裁，哪怕版本 2 不会动它。
	h := toolTurn("c1", "bash", `{}`, strings.Repeat("x", 700))
	if PruneToolOutputs(h, 0, 0, 1) != 1 {
		t.Error("版本 1 下 700 字符的输出应被裁剪")
	}
}

func TestPruneHeadAndTailIsIdempotent(t *testing.T) {
	h := toolTurn("c1", "bash", `{}`, numberedLines(100, 40))
	PruneToolOutputs(h, 0, 0, PruneVersion)
	once := h[1].Text()
	if PruneToolOutputs(h, 0, 0, PruneVersion) != 0 || h[1].Text() != once {
		t.Fatal("已经裁过的不应再动")
	}
}
