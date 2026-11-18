package agent

import (
	"fmt"
	"regexp"
	"strings"
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

	// 裁剪旧工具输出：只动不在最近 keepRecentMsgs 条里的 tool 消息。保留多少由 prunePolicy 按工具决定，
	// 原文至少比保留部分长 pruneMinSaving 个字符才裁，省得为几百字改动历史、让前缀缓存失效。
	pruneMinSaving = 400
	keepRecentMsgs = 6

	// 主动裁剪的目标是触发线的这个比例，而不是刚好压到触发线以下：
	// 否则下一步新增的几条消息又会越线，一轮里每一步都在裁。
	pruneHeadroom = 0.85
)

// pruneMarker 标记只保留了开头的工具输出，独占最后一行；%s 是附加说明（如提示可以重新读取）。
// pruneMidMarker 标记保留了开头和结尾的工具输出，单独占中间一行。两者都用来保证裁剪是幂等的。
const (
	pruneMarker    = "…[旧输出已裁剪以节省上下文，原文共 %d 字符%s]"
	pruneMidMarker = "…[中间 %d 字符已裁剪以节省上下文，原文共 %d 字符]"
)

var (
	rePruned    = regexp.MustCompile(`\n…\[旧输出已裁剪以节省上下文，原文共 \d+ 字符[^\]\n]*\]$`)
	rePrunedMid = regexp.MustCompile(`\n…\[中间 \d+ 字符已裁剪以节省上下文，原文共 \d+ 字符\]\n`)
)

// PruneVersion 是运行时当前使用的裁剪策略版本，随 Compacted 事件落盘。会话恢复时按记录里的版本重放，
// 旧会话（没有版本，即 1）仍按当年的统一策略裁，恢复出来的历史与当初一致。改 prunePolicy 时要加版本号。
const PruneVersion = 2

// prunePolicy 是一类工具输出被裁剪时保留的内容：开头 head 个字符、结尾 tail 个字符（按整行取），
// note 追加在只留开头的标记里。
type prunePolicy struct {
	head, tail int
	note       string
}

func (p prunePolicy) minChars() int { return p.head + p.tail + pruneMinSaving }

// policyFor 返回 version 版本的策略下，name 工具的输出怎么裁。
//
// 版本 1 对所有工具统一保留开头 200 字符。版本 2 按工具区分：
//   - read：文件内容随时能重新读，只留开头（路径和行号范围），并提示需要时重读；
//   - agent：子代理的报告是它整个调查的结论，没法廉价地重来，开头结尾都多留；
//   - mcp__*：结果多是 JSON 或文档，关键信息通常在前面，只留开头；
//   - 其余（bash、powershell、外部命令等）：命令输出的报错、测试汇总、退出原因往往在最后，留开头和结尾。
//
// 工具名是字面量：agent 包不能 import subagent / mcp（它们依赖 agent）。
func policyFor(version int, name string) prunePolicy {
	if version < 2 {
		return prunePolicy{head: 200}
	}
	switch {
	case name == "read":
		return prunePolicy{head: 300, note: "；需要时用 read 重新读取"}
	case name == "agent":
		return prunePolicy{head: 1500, tail: 500}
	case strings.HasPrefix(name, "mcp__"):
		return prunePolicy{head: 500}
	}
	return prunePolicy{head: 300, tail: 500}
}

// estimateText 是 EstimateTokens 在内核里的简写。
func estimateText(s string) int { return EstimateTokens(s) }

// estimateMessage 估算一条历史消息占用的 token。图片按尺寸折算，文本按字符类别折算。
func estimateMessage(m Message) int {
	n := msgOverhead + estimateText(m.Reasoning)
	for _, blk := range m.Content {
		if blk.Type == BlockImage {
			n += EstimateImageTokens(blk.MediaType, blk.Data)
		} else {
			n += estimateText(blk.Text)
		}
	}
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

// pruneTarget 是一条可以裁剪的 tool 消息及其策略。
type pruneTarget struct {
	idx    int
	policy prunePolicy
}

// pruneCandidates 返回可以被裁剪的 tool 消息，从旧到新：不在最近 keepRecent 条里、比策略保留的部分长得多、
// 还没裁过、不带图片（只截文本会把图片和文字的对应关系弄乱）、不是技能正文。
func pruneCandidates(history []Message, keepRecent, version int) []pruneTarget {
	var out []pruneTarget
	names, skill := toolCallInfo(history)
	for i := 0; i < len(history)-keepRecent; i++ {
		m := history[i]
		if m.Role != RoleTool || m.HasImages() || skill[m.ToolID] {
			continue
		}
		p := policyFor(version, names[m.ToolID])
		if text := m.Text(); utf8.RuneCountInString(text) > p.minChars() && !isPruned(text) {
			out = append(out, pruneTarget{i, p})
		}
	}
	return out
}

// toolCallInfo 返回历史里每个调用 id 对应的工具名，以及读取技能正文（read 工具读 SKILL.md）的调用 id。
// 技能正文是模型按步骤执行任务的依据，被裁成开头一小段后任务会做偏，所以不参与裁剪。
// 只看历史里的调用参数，会话恢复重放时得到同样的结果，裁剪与当初一致。
func toolCallInfo(history []Message) (names map[string]string, skill map[string]bool) {
	names, skill = map[string]string{}, map[string]bool{}
	for _, m := range history {
		if m.Role != RoleAssistant {
			continue
		}
		for _, c := range m.ToolCalls {
			names[c.ID] = c.Name
			if c.Name == "read" && strings.Contains(c.Arguments, "SKILL.md") {
				skill[c.ID] = true
			}
		}
	}
	return names, skill
}

// pruneOne 按策略把一条 tool 消息裁成“开头 + 标记”或“开头 + 标记 + 结尾”，返回估算省下的 token。
func pruneOne(m *Message, p prunePolicy) int {
	before := estimateMessage(*m)
	runes := []rune(m.Text())
	total := len(runes)
	head := string(runes[:p.head])
	if p.tail <= 0 {
		m.Content = []Block{TextBlock(head + "\n" + fmt.Sprintf(pruneMarker, total, p.note))}
		return before - estimateMessage(*m)
	}
	tail := string(runes[total-p.tail:])
	// 结尾从整行开始：半截的行既难读，也可能把一个路径或数字切成看似完整的另一个。
	if i := strings.IndexByte(tail, '\n'); i >= 0 && i < len(tail)-1 {
		tail = tail[i+1:]
	}
	omitted := total - p.head - utf8.RuneCountInString(tail)
	m.Content = []Block{TextBlock(head + "\n" + fmt.Sprintf(pruneMidMarker, omitted, total) + "\n" + tail)}
	return before - estimateMessage(*m)
}

// PruneToolOutputs 按 version 版本的策略，把 keepRecent 条之前的、较长的 tool 消息从最旧的开始裁剪，
// 返回被裁剪的条数。limit > 0 时最多裁 limit 条，<= 0 表示全部。就地修改 history。
// 幂等：已经裁剪过的不会再动。
//
// 它是纯函数（只看 history 和参数），所以会话恢复时用记录里的条数和版本重放同样的调用，
// 能得到和当初完全一致的历史。旧版本的运行时一次裁光所有候选，记录里的条数就是候选总数，
// 重放时同样裁光，结果不变。
func PruneToolOutputs(history []Message, keepRecent, limit, version int) int {
	pruned := 0
	for _, c := range pruneCandidates(history, keepRecent, version) {
		if limit > 0 && pruned >= limit {
			break
		}
		pruneOne(&history[c.idx], c.policy)
		pruned++
	}
	if pruned > 0 {
		DropNative(history) // 改了中间的工具结果，之后所有思考块绑定的前缀都变了
	}
	return pruned
}

// pruneToward 按当前策略从最旧的开始裁剪，直到估算省下至少 need 个 token 或没有可裁的。
// 返回裁剪条数、省下的估算 token 总数，以及其中落在 history[:calibLen] 里的部分
// （那一段的 token 数来自服务端，调用方要据此扣减校准值）。
func pruneToward(history []Message, keepRecent, need, calibLen int) (pruned, saved, savedCalibrated int) {
	for _, c := range pruneCandidates(history, keepRecent, PruneVersion) {
		if saved >= need {
			break
		}
		d := pruneOne(&history[c.idx], c.policy)
		pruned++
		saved += d
		if c.idx < calibLen {
			savedCalibrated += d
		}
	}
	if pruned > 0 {
		DropNative(history)
	}
	return pruned, saved, savedCalibrated
}

// DropNative 清掉历史里全部协议原始内容，文本、思维链文本和工具调用保留。
//
// 原始思考块的签名绑定了产生它时的完整前缀（系统提示词、工具、之前的全部消息）。压缩改写了历史、
// 恢复会话换了系统提示词（里面有日期）之后再回传，会被服务端判定为改过的历史：新账号直接 400，
// 老账号也会被丢弃。去掉思考块是服务端文档给出的做法——从前面删块是允许的，改中间不行。
func DropNative(history []Message) {
	for i := range history {
		history[i].Native = nil
	}
}

func isPruned(s string) bool { return rePruned.MatchString(s) || rePrunedMid.MatchString(s) }

// PruneKeepRecent 是压缩流程里裁剪旧工具输出时保护的最近消息条数，导出给会话恢复重放用。
const PruneKeepRecent = keepRecentMsgs
