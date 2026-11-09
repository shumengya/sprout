package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

const welcomeText = `输入问题后按 Enter 发送；PgUp / PgDn 或鼠标滚轮可以翻看历史；/help 查看全部快捷键。`

const helpText = `快捷键
  Enter           发送
  Ctrl+J          输入框换行
  Ctrl+C          生成中：取消本轮；空闲：退出
  Ctrl+D          退出

查看历史
  鼠标滚轮 / PgUp PgDn    滚动对话
  Ctrl+↑ Ctrl+↓           逐行滚动
  Ctrl+Home Ctrl+End      跳到最前 / 最后
  ↑ ↓ Home End            输入框为空时同上
  滚到底部后会自动跟随新内容；上滚期间状态行会提示“有新内容”

命令
  /resume         列出当前目录的历史会话；/resume 序号 恢复其中一个
  /think          展开 / 折叠模型的思考过程
  /mouse          开关鼠标滚轮（关闭后可直接用鼠标选中文字复制）
  /tools          查看检测到的终端工具
  /help           显示本帮助
  /quit           退出`

// slash 处理本地斜杠命令。只匹配第一个词，且仅限已知命令：
// 其他以 / 开头的输入（比如路径）会原样交给模型。
func (m *Model) slash(text string) (handled bool, cmd tea.Cmd) {
	name, arg, _ := strings.Cut(text, " ")
	switch name {
	case "/resume":
		m.follow = true
		return true, m.resumeCmd(strings.TrimSpace(arg))
	case "/help":
		m.add(&entry{kind: kindNotice, text: helpText})
	case "/tools":
		m.add(&entry{kind: kindNotice, text: m.toolsReport()})
	case "/think":
		m.showThink = !m.showThink
		state := "已折叠，只显示一行摘要"
		if m.showThink {
			state = "已展开，显示完整内容"
		}
		m.add(&entry{kind: kindNotice, text: "模型的思考过程" + state + "。"})
	case "/mouse":
		m.mouse = !m.mouse
		msg := "鼠标滚轮已关闭，现在可以直接用鼠标选中文字复制；翻页请用 PgUp / PgDn。"
		if m.mouse {
			msg = "鼠标滚轮已开启，可滚动对话。此时选中文字需要按住 Shift 再拖动（取决于终端）。"
		}
		m.add(&entry{kind: kindNotice, text: msg})
	case "/quit", "/exit":
		return true, tea.Quit
	default:
		return false, nil
	}
	m.follow = true
	return true, nil
}

func (m *Model) toolsReport() string {
	info := m.opts.Info
	var b strings.Builder
	fmt.Fprintf(&b, "模型: %s（%s）\n工作目录: %s\n数据目录: %s\n", info.Model, info.Provider, info.Cwd, info.DataDir)
	if len(info.Tools) == 0 {
		b.WriteString("可用终端工具: 无")
	} else {
		b.WriteString("可用终端工具:")
		for _, t := range info.Tools {
			fmt.Fprintf(&b, "\n  %s  %s", t.Name, t.Detail)
		}
	}
	for _, u := range info.Unavailable {
		b.WriteString("\n  " + u)
	}
	return b.String()
}
