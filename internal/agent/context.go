package agent

import (
	"fmt"
	"regexp"
	"unicode/utf8"
)

// 上下文管理的常量。都是经验值，宁可偏保守：高估 token 只会让压缩早一点发生，低估才会撞上窗口。
const (
	// 窗口里留给模型输出的比例上限；剩余部分的 compactRatio 作为触发压缩的预算。
	reserveRatio = 0.25
	compactRatio = 0.80
	// 压缩后希望保留的最近对话占预算的比例。
	tailRatio = 0.40

	// 每条消息、每个 tool call 的结构开销（角色、分隔、JSON 包装）。
	msgOverhead  = 4
	callOverhead = 12

	// 裁剪旧工具输出：只动超过这个长度的、且不在最近 keepRecentMsgs 条里的 tool 消息。
	pruneMinChars  = 600
	pruneKeepChars = 200
	keepRecentMsgs = 6
)

// pruneMarker 标记已被裁剪的工具输出，独占最后一行，也用来保证裁剪是幂等的。
const pruneMarker = "…[旧输出已裁剪以节省上下文，原文共 %d 字符]"

var rePruned = regexp.MustCompile(`\n…\[旧输出已裁剪以节省上下文，原文共 \d+ 字符\]$`)

// estimateText 估算一段文本的 token 数：ASCII 约 3 字符/token，宽字符（中日韩）约 1 字符/token。
func estimateText(s string) int {
	ascii, wide := 0, 0
	for _, r := range s {
		if r < 0x80 {
			ascii++
		} else {
			wide++
		}
	}
	return (ascii+2)/3 + wide
}

// estimateMessage 估算一条历史消息占用的 token。
func estimateMessage(m Message) int {
	n := msgOverhead + estimateText(m.Content) + estimateText(m.Reasoning)
	for _, c := range m.ToolCalls {
		n += callOverhead + estimateText(c.Name) + estimateText(c.Arguments)
	}
	return n
}

func estimateMessages(ms []Message) int {
	n := 0
	for _, m := range ms {
		n += estimateMessage(m)
	}
	return n
}

// contextBudget 返回触发压缩的 token 预算。window <= 0 表示不知道窗口大小，返回 0（不做主动压缩）。
func contextBudget(window, maxOutput int) int {
	if window <= 0 {
		return 0
	}
	reserve := min(maxOutput, int(float64(window)*reserveRatio))
	return int(float64(window-reserve) * compactRatio)
}

// PruneToolOutputs 把 keepRecent 条之前的、较长的 tool 消息裁剪成“开头 + 标记”，返回被裁剪的条数。
// 就地修改 history。幂等：已经裁剪过的不会再动。
// 它是纯函数（只看 history），所以会话恢复时重放同样的调用能得到和当初完全一致的历史。
func PruneToolOutputs(history []Message, keepRecent int) int {
	pruned := 0
	limit := len(history) - keepRecent
	for i := 0; i < limit; i++ {
		m := &history[i]
		if m.Role != RoleTool || utf8.RuneCountInString(m.Content) <= pruneMinChars || isPruned(m.Content) {
			continue
		}
		total := utf8.RuneCountInString(m.Content)
		head := string([]rune(m.Content)[:pruneKeepChars])
		m.Content = head + "\n" + fmt.Sprintf(pruneMarker, total)
		pruned++
	}
	return pruned
}

func isPruned(s string) bool { return rePruned.MatchString(s) }

// PruneKeepRecent 是 PruneToolOutputs 在压缩流程里使用的 keepRecent 值，导出给会话恢复重放用。
const PruneKeepRecent = keepRecentMsgs
