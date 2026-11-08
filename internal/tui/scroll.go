package tui

// 滚轮每格滚动的行数。
const wheelStep = 3

// scrollBy 滚动对话区，n > 0 向下、n < 0 向上，并同步“是否跟随底部”。
func (m *Model) scrollBy(n int) {
	if n > 0 {
		m.vp.ScrollDown(n)
	} else if n < 0 {
		m.vp.ScrollUp(-n)
	}
	m.syncFollow()
}

func (m *Model) scrollPage(down bool) {
	if down {
		m.vp.PageDown()
	} else {
		m.vp.PageUp()
	}
	m.syncFollow()
}

func (m *Model) scrollTop() {
	m.vp.GotoTop()
	m.syncFollow()
}

func (m *Model) scrollBottom() {
	m.vp.GotoBottom()
	m.syncFollow()
}

// syncFollow 根据当前位置决定是否自动跟随：回到底部即恢复跟随，并清除“有新内容”。
func (m *Model) syncFollow() {
	m.follow = m.vp.AtBottom()
	if m.follow {
		m.unseen = false
	}
}

// scrollKey 处理滚动相关的按键，返回是否已处理。
// 方向键、Home、End 只在输入框为空时用于滚动，否则仍交给输入框移动光标。
func (m *Model) scrollKey(k string) bool {
	switch k {
	case "pgup":
		m.scrollPage(false)
	case "pgdown":
		m.scrollPage(true)
	case "ctrl+up":
		m.scrollBy(-1)
	case "ctrl+down":
		m.scrollBy(1)
	case "ctrl+home":
		m.scrollTop()
	case "ctrl+end":
		m.scrollBottom()
	case "up", "down", "home", "end":
		if m.in.Value() != "" {
			return false
		}
		switch k {
		case "up":
			m.scrollBy(-1)
		case "down":
			m.scrollBy(1)
		case "home":
			m.scrollTop()
		case "end":
			m.scrollBottom()
		}
	default:
		return false
	}
	return true
}
