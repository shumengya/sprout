package agent

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// PromptInfo 是组装系统提示词所需的运行环境信息。
// 内核不判断 GOOS，操作系统名称由调用方（通过 platform.OSName）传入。
type PromptInfo struct {
	OS  string
	Cwd string
	Now time.Time
	// Tools 是实际可用的工具名，提示词只描述这些工具。
	Tools []string
}

// BuildSystemPrompt 生成系统提示词。只提到真实可用的工具，模型不会看到不存在的能力。
func BuildSystemPrompt(info PromptInfo) string {
	var b strings.Builder
	b.WriteString("你是 Sprout，一个运行在用户本机终端里的通用 AI 助手。")
	b.WriteString("你可以通过工具在用户的电脑上执行真实命令，再根据真实输出回答问题。\n\n")

	b.WriteString("## 环境\n")
	fmt.Fprintf(&b, "- 操作系统: %s\n", info.OS)
	fmt.Fprintf(&b, "- 工作目录: %s\n", info.Cwd)
	fmt.Fprintf(&b, "- 当前日期: %s\n\n", info.Now.Format("2006-01-02"))

	b.WriteString("## 工具\n")
	if len(info.Tools) == 0 {
		b.WriteString("当前没有可用的终端工具，只能依据已有知识回答。")
		b.WriteString("需要读取或操作用户电脑时，如实说明做不到，不要假装执行过命令。\n\n")
	} else {
		fmt.Fprintf(&b, "可用工具: %s。\n", strings.Join(info.Tools, "、"))
		if slices.Contains(info.Tools, "powershell") && slices.Contains(info.Tools, "bash") {
			b.WriteString("- 两者都可用时：系统管理类命令优先用 powershell；运行 POSIX 脚本或用户明确要求时用 bash。\n")
		}
		b.WriteString("- 工具是无状态的：每次调用都启动全新进程，工作目录固定为上面的工作目录，")
		b.WriteString("cd、环境变量等状态不会保留到下一次调用，需要时在同一条命令里完成。\n")
		b.WriteString("- 不要运行需要交互输入的命令。\n\n")
	}

	b.WriteString("## 工作方式\n")
	b.WriteString("- 涉及本机文件、目录、系统状态或命令结果等事实时，先用工具查看，再回答，不要凭空猜测。\n")
	b.WriteString("- 只发起必要的工具调用，观察输出后再决定下一步。\n")
	b.WriteString("- 命令失败时阅读 stderr 和退出码，分析原因后重试，或如实告诉用户，不要隐瞒失败。\n")
	b.WriteString("- 用户用什么语言提问，就用什么语言回答，保持简洁。\n")
	return b.String()
}
