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

复制
  在对话区按住鼠标左键拖动选中文字，松开自动复制；拖到对话区上下边缘会自动滚动
  按任意键或点击别处取消选中

命令
  /model              打开模型选择器，切换后保留对话历史；/model 引用 直接切换
  /effort             调整思考程度（档位来自 models.jsonc）；/effort 档位 直接切换，生成中也能改，下次请求生效
  /new                开始新会话（清空对话区和上下文，旧会话可用 /resume 找回）
  /image 路径          附加一张图片到下一条消息；/image 查看待发送的图片，/image clear 清空
                      消息里直接写现有图片文件的路径（png / jpeg / gif / webp）也会自动附上
  /resume             列出当前目录的历史会话；/resume 序号 恢复其中一个
  /think              展开 / 折叠全部思考过程；点击单条思考过程可以单独展开或收起
  /mouse              开关鼠标上报（滚轮与拖选复制；关闭后改用终端自带的选中）
  /tools              查看检测到的终端工具
  /install-sprout     把当前程序注册成全局 sprout 命令（新开的终端里生效）
  /uninstall-sprout   卸载全局 sprout 命令（程序文件不动）
  /help               显示本帮助
  /quit               退出`

// slash 处理本地斜杠命令。只匹配第一个词，且仅限已知命令：
// 其他以 / 开头的输入（比如路径）会原样交给模型。
func (m *Model) slash(text string) (handled bool, cmd tea.Cmd) {
	name, arg, _ := strings.Cut(text, " ")
	switch name {
	case "/model":
		m.follow = true
		return true, m.modelCmd(strings.TrimSpace(arg))
	case "/resume":
		m.follow = true
		return true, m.resumeCmd(strings.TrimSpace(arg))
	case "/effort":
		m.effortCmd(strings.TrimSpace(arg))
	case "/new":
		m.newSessionCmd()
	case "/image":
		m.imageCmd(strings.TrimSpace(arg))
	case "/install-sprout", "/uninstall-sprout":
		m.follow = true
		return true, m.installCmd(name == "/uninstall-sprout")
	case "/help":
		m.add(&entry{kind: kindNotice, text: helpText})
	case "/tools":
		m.add(&entry{kind: kindNotice, text: m.toolsReport()})
	case "/think":
		m.showThink = !m.showThink
		for _, e := range m.entries {
			if e.kind == kindThink && e.open != m.showThink {
				e.open = m.showThink
				e.touch()
			}
		}
		state := "已折叠全部思考过程，点击单条可以展开。"
		if m.showThink {
			state = "已展开全部思考过程，点击单条可以收起。"
		}
		m.add(&entry{kind: kindNotice, text: state})
	case "/mouse":
		m.mouse = !m.mouse
		m.clearSel()
		msg := "鼠标滚轮已关闭，现在可以直接用鼠标选中文字复制；翻页请用 PgUp / PgDn。"
		if m.mouse {
			msg = "鼠标滚轮已开启，可滚动对话；在对话区拖动可选中并复制。\n终端自带的选中需要按住 Shift 再拖动（取决于终端）。"
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
