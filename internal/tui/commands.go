package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

const helpText = `快捷键
  Enter           发送
  Ctrl+J          输入框换行
  Ctrl+C          生成中：取消本轮；空闲：退出
  Ctrl+D          退出
  PgUp / PgDn     滚动对话

命令
  /tools          查看检测到的终端工具
  /help           显示本帮助
  /quit           退出`

// slash 处理本地斜杠命令。只匹配第一个词，且仅限已知命令：
// 其他以 / 开头的输入（比如路径）会原样交给模型。
func (m *Model) slash(text string) (handled bool, cmd tea.Cmd) {
	name, _, _ := strings.Cut(text, " ")
	switch name {
	case "/help":
		m.add(&entry{kind: kindNotice, text: helpText})
	case "/tools":
		m.add(&entry{kind: kindNotice, text: m.toolsReport()})
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
		b.WriteString("\n  ✗ " + u)
	}
	return b.String()
}
