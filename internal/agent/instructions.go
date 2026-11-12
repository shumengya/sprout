package agent

import "strings"

// Instruction 是一份用户写给 agent 的说明（AGENTS.md），按作用范围从宽到窄排列后拼进系统提示词。
type Instruction struct {
	Path  string // 文件绝对路径，原样告诉模型，方便它判断这份说明管哪个目录
	Scope string // "全局" 或 "项目"
	Text  string
}

// AppendInstructions 把说明文件追加在系统提示词末尾。
//
// 放在末尾而不是交给 SYSTEMPROMPT.md 模板决定位置：用户的模板可能是旧版本或完全自写的，
// 做成模板变量的话，没写这个变量的模板会让 AGENTS.md 悄无声息地失效。
// 每份用带路径的标签包起来，模型能分清哪段来自哪个文件、管哪个目录。
func AppendInstructions(system string, docs []Instruction) string {
	if len(docs) == 0 {
		return system
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(system, "\n"))
	b.WriteString("\n\n## 用户与项目说明\n")
	b.WriteString("下面是用户在 AGENTS.md 里写给你的说明，按作用范围从宽到窄排列：全局说明适用于所有目录，" +
		"项目说明适用于它所在的目录及其子目录。说明之间冲突时以靠后（更具体）的为准；" +
		"用户在对话里直接说的话优先于这些文件。\n")
	for _, d := range docs {
		b.WriteString("\n<instructions scope=\"")
		b.WriteString(d.Scope)
		b.WriteString("\" path=\"")
		b.WriteString(d.Path)
		b.WriteString("\">\n")
		b.WriteString(strings.TrimSpace(d.Text))
		b.WriteString("\n</instructions>\n")
	}
	return b.String()
}
