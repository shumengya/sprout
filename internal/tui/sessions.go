package tui

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// 会话浏览器（/resume）：替换输入框，上面是两行按键提示和搜索行，下面是会话列表。
//
//	恢复会话（当前目录）                 ◉ 当前目录 │ ○ 全部   名称：全部  排序：最近
//	Tab 范围 · re:<正则> 正则 · "短语" 精确 · ↑↓ 选择 · Enter 恢复 · Esc 关闭
//	Ctrl+S 排序 · Ctrl+N 已命名 · Ctrl+R 重命名 · Ctrl+D 删除 · Ctrl+P 路径（关）
//	> 搜索文字
//	❯ 系统提示词的英文怎么说                                       3 轮  3小时前
//	  帮我搜索一下去年的国产旗舰手机                              50 轮  4小时前
//	  (1/60)

type browseMode int

const (
	modeBrowse  browseMode = iota
	modeRename             // 编辑名字
	modeConfirm            // 确认删除
)

// 排序方式，Ctrl+S 依次切换。
var sortNames = []string{"最近", "最早", "轮数"}

type sessionBrowser struct {
	all      bool // 范围：全部目录 / 当前目录
	loading  bool
	loadErr  string
	items    []SessionInfo
	seq      int    // 加载批次，丢弃过期的结果
	keep     string // 重新加载后要停留的会话路径
	query    string
	sortBy   int
	named    bool // 只看起过名字的
	showPath bool
	cursor   int // visible 里的下标
	offset   int // 列表滚动位置

	mode   browseMode
	edit   string      // 重命名时正在编辑的名字
	target SessionInfo // 重命名或删除的对象
	note   string      // 最近一次操作的结果，显示在底部
	noteOK bool
}

type sessionsLoadedMsg struct {
	seq  int
	list []SessionInfo
	err  error
}

type sessionOpDoneMsg struct {
	note string
	err  error
}

// loadSessions 异步读取会话列表。
func (m *Model) loadSessions() tea.Cmd {
	b := m.browser
	b.seq++
	b.loading = true
	seq, all, s := b.seq, b.all, m.opts.Sessions
	return func() tea.Msg {
		list, err := s.List(all)
		return sessionsLoadedMsg{seq, list, err}
	}
}

func (m *Model) onSessionsLoaded(msg sessionsLoadedMsg) {
	b := m.browser
	if b == nil || msg.seq != b.seq {
		return // 浏览器已关闭，或者是切换范围之前发出的请求
	}
	b.loading, b.items, b.loadErr = false, msg.list, ""
	if msg.err != nil {
		b.items, b.loadErr = nil, "读取会话列表失败："+msg.err.Error()
	}
	b.cursor = 0
	if b.keep != "" {
		if i := slices.IndexFunc(b.visible(), func(it SessionInfo) bool { return it.Path == b.keep }); i >= 0 {
			b.cursor = i
		}
		b.keep = ""
	}
}

// onSessionOp 处理重命名、删除的结果，然后重新加载列表。
func (m *Model) onSessionOp(msg sessionOpDoneMsg) tea.Cmd {
	b := m.browser
	if b == nil {
		return nil
	}
	b.note, b.noteOK = msg.note, msg.err == nil
	if msg.err != nil {
		b.note = msg.err.Error()
	}
	return m.loadSessions()
}

// matcher 按搜索词构造匹配函数，参数是一个会话的各个可搜索字段（名字、提问、模型、ID、目录）。
// 都不区分大小写：
//   - re:前缀是正则，任一字段匹配即可，所以 ^ $ 对应的是字段的开头结尾；
//   - 整体加了双引号是精确短语，任一字段包含即可；
//   - 其余按空白拆成关键词，全部出现即可，关键词可以分布在不同字段里（如名字一个、提问一个）。
//
// 正则写错时返回错误说明，列表显示为空。
func (b *sessionBrowser) matcher() (func([]string) bool, string) {
	q := strings.TrimSpace(b.query)
	anyField := func(f func(string) bool) func([]string) bool {
		return func(fields []string) bool { return slices.ContainsFunc(fields, f) }
	}
	switch {
	case q == "":
		return func([]string) bool { return true }, ""
	case strings.HasPrefix(q, "re:"):
		re, err := regexp.Compile("(?i)" + strings.TrimPrefix(q, "re:"))
		if err != nil {
			return func([]string) bool { return false }, "正则无效：" + err.Error()
		}
		return anyField(re.MatchString), ""
	case len(q) >= 2 && strings.HasPrefix(q, `"`) && strings.HasSuffix(q, `"`):
		phrase := strings.ToLower(q[1 : len(q)-1])
		return anyField(func(s string) bool { return strings.Contains(strings.ToLower(s), phrase) }), ""
	}
	terms := strings.Fields(strings.ToLower(q))
	return func(fields []string) bool {
		s := strings.ToLower(strings.Join(fields, "\n"))
		for _, t := range terms {
			if !strings.Contains(s, t) {
				return false
			}
		}
		return true
	}, ""
}

// visible 返回过滤、排序后的会话。
func (b *sessionBrowser) visible() []SessionInfo {
	match, _ := b.matcher()
	var out []SessionInfo
	for _, it := range b.items {
		if b.named && it.Name == "" {
			continue
		}
		if match([]string{it.Name, it.Prompt, it.Model, it.ID, it.Cwd}) {
			out = append(out, it)
		}
	}
	switch b.sortBy {
	case 1:
		slices.SortStableFunc(out, func(x, y SessionInfo) int { return x.Updated.Compare(y.Updated) })
	case 2:
		slices.SortStableFunc(out, func(x, y SessionInfo) int { return y.Turns - x.Turns })
	}
	return out // 加载结果本来就是最近使用在前
}

func (b *sessionBrowser) selected() (SessionInfo, bool) {
	v := b.visible()
	if len(v) == 0 {
		return SessionInfo{}, false
	}
	return v[min(b.cursor, len(v)-1)], true
}

func (b *sessionBrowser) move(d int) {
	n := len(b.visible())
	b.cursor = min(max(b.cursor+d, 0), max(n-1, 0))
}

// sessionKey 在浏览器打开时处理按键，返回 true 表示已消费。
func (m *Model) sessionKey(k tea.KeyPressMsg) (bool, tea.Cmd) {
	b := m.browser
	if b == nil {
		return false, nil
	}
	key := k.String()
	switch b.mode {
	case modeConfirm:
		b.mode = modeBrowse
		if k.Text == "y" || k.Text == "Y" {
			return true, m.sessionOp(b.target, "")
		}
		b.note, b.noteOK = "已取消删除", false
		return true, nil
	case modeRename:
		switch key {
		case "esc", "ctrl+c":
			b.mode, b.note, b.noteOK = modeBrowse, "已取消重命名", false
		case "enter":
			b.mode = modeBrowse
			name := strings.TrimSpace(b.edit)
			return true, m.sessionOp(b.target, "rename:"+name)
		case "backspace":
			if r := []rune(b.edit); len(r) > 0 {
				b.edit = string(r[:len(r)-1])
			}
		case "ctrl+u":
			b.edit = ""
		default:
			b.edit += k.Text
		}
		return true, nil
	}

	b.note = ""
	switch key {
	case "esc", "ctrl+c":
		m.browser = nil
		m.status = "已取消恢复会话"
	case "up":
		b.move(-1)
	case "down":
		b.move(1)
	case "pgup":
		b.move(-m.browserRows())
	case "pgdown":
		b.move(m.browserRows())
	case "home":
		b.cursor = 0
	case "end":
		b.move(1 << 30)
	case "tab":
		b.all = !b.all
		if sel, ok := b.selected(); ok {
			b.keep = sel.Path
		}
		return true, m.loadSessions()
	case "ctrl+s":
		b.sortBy, b.cursor = (b.sortBy+1)%len(sortNames), 0
	case "ctrl+n":
		b.named, b.cursor = !b.named, 0
	case "ctrl+p":
		b.showPath = !b.showPath
	case "ctrl+r":
		if sel, ok := b.selected(); ok {
			b.mode, b.target, b.edit = modeRename, sel, sel.Name
		}
	case "ctrl+d":
		sel, ok := b.selected()
		switch {
		case !ok:
		case sel.Current:
			b.note, b.noteOK = "这是当前正在使用的会话，不能删除；先 /new 开一个新会话", false
		default:
			b.mode, b.target = modeConfirm, sel
		}
	case "enter":
		sel, ok := b.selected()
		if !ok {
			return true, nil
		}
		m.browser = nil
		if sel.Current {
			m.status = "已经在这个会话里了"
			return true, nil
		}
		m.follow = true
		return true, m.startResume(sel.Path)
	case "backspace":
		if r := []rune(b.query); len(r) > 0 {
			b.query, b.cursor = string(r[:len(r)-1]), 0
		}
	case "ctrl+u":
		b.query, b.cursor = "", 0
	default:
		if k.Text != "" {
			b.query, b.cursor = b.query+k.Text, 0
		}
	}
	return true, nil
}

// sessionOp 异步执行重命名（op 为 "rename:名字"）或删除（op 为空），完成后重新加载列表。
func (m *Model) sessionOp(target SessionInfo, op string) tea.Cmd {
	s := m.opts.Sessions
	m.browser.keep = target.Path
	title := oneLineTitle(target.Title(), 20)
	return func() tea.Msg {
		if name, ok := strings.CutPrefix(op, "rename:"); ok {
			if err := s.Rename(target.Path, name); err != nil {
				return sessionOpDoneMsg{err: fmt.Errorf("重命名失败：%w", err)}
			}
			if name == "" {
				return sessionOpDoneMsg{note: "已清除「" + title + "」的名字"}
			}
			return sessionOpDoneMsg{note: "已重命名为「" + name + "」"}
		}
		if err := s.Delete(target.Path); err != nil {
			return sessionOpDoneMsg{err: fmt.Errorf("删除失败：%w", err)}
		}
		return sessionOpDoneMsg{note: "已删除「" + title + "」"}
	}
}

// pasteIntoBrowser 把粘贴的文字放进搜索框或名字编辑框；浏览器没打开时返回 false。
func (m *Model) pasteIntoBrowser(text string) bool {
	b := m.browser
	if b == nil {
		return false
	}
	text = strings.Join(strings.Fields(text), " ")
	switch b.mode {
	case modeRename:
		b.edit += text
	case modeBrowse:
		b.query, b.cursor = b.query+text, 0
	}
	return true
}

// browserRows 是列表最多显示的行数：屏幕越高显示越多，但至少 3 行，最多 12 行。
func (m *Model) browserRows() int { return min(max(m.h-14, 3), 12) }

var (
	styBrowseTitle = lipgloss.NewStyle().Bold(true)
	styBrowseSel   = lipgloss.NewStyle().Background(lipgloss.Color("24")).Foreground(lipgloss.Color("15"))
)

// browserView 渲染会话浏览器，替换输入框的位置。
func (m *Model) browserView(w int) string {
	b := m.browser
	var lines []string

	scope := "当前目录"
	if b.all {
		scope = "全部目录"
	}
	radio := func(on bool, s string) string {
		if on {
			return styInfoThink.Render("◉ " + s)
		}
		return styFaint.Render("○ " + s)
	}
	named := "全部"
	if b.named {
		named = "已命名"
	}
	right := radio(!b.all, "当前目录") + styFaint.Render(" │ ") + radio(b.all, "全部") +
		styFaint.Render("   名称：") + styInfoThink.Render(named) +
		styFaint.Render("  排序：") + styInfoThink.Render(sortNames[b.sortBy])
	lines = append(lines, spread(styBrowseTitle.Render("恢复会话（"+scope+"）"), right, w))

	path := "关"
	if b.showPath {
		path = "开"
	}
	lines = append(lines,
		styFaint.Render(`Tab 范围 · re:<正则> 正则 · "短语" 精确 · ↑↓ 选择 · Enter 恢复 · Esc 关闭`),
		styFaint.Render("Ctrl+S 排序 · Ctrl+N 已命名 · Ctrl+R 重命名 · Ctrl+D 删除 · Ctrl+P 路径（"+path+"）"))

	caret := styReverse.Render(" ")
	switch b.mode {
	case modeRename:
		lines = append(lines, styWarn.Render("重命名（Enter 保存，留空清除名字，Esc 取消）> ")+b.edit+caret)
	default:
		lines = append(lines, "> "+b.query+caret)
	}

	_, qerr := b.matcher()
	v := b.visible()
	rows := m.browserRows()
	switch {
	case b.loading && len(b.items) == 0:
		lines = append(lines, styFaint.Render("  正在读取会话…"))
	case b.loadErr != "":
		lines = append(lines, styErr.Render("  "+b.loadErr))
	case qerr != "":
		lines = append(lines, styErr.Render("  "+qerr))
	case len(b.items) == 0 && !b.all:
		lines = append(lines, styFaint.Render("  当前目录下还没有会话，按 Tab 查看全部目录。"))
	case len(b.items) == 0:
		lines = append(lines, styFaint.Render("  还没有任何会话。"))
	case len(v) == 0:
		lines = append(lines, styFaint.Render("  没有匹配的会话。"))
	default:
		b.cursor = min(b.cursor, len(v)-1)
		// 光标移出可见区域时才滚动，翻看时列表不会每按一下就跳。
		if b.cursor < b.offset {
			b.offset = b.cursor
		}
		if b.cursor >= b.offset+rows {
			b.offset = b.cursor - rows + 1
		}
		b.offset = max(min(b.offset, len(v)-rows), 0)
		now := m.now()
		for i := b.offset; i < min(b.offset+rows, len(v)); i++ {
			lines = append(lines, b.row(v[i], i == b.cursor, now, w))
		}
	}

	var foot string
	if len(v) > 0 {
		foot = styFaint.Render(fmt.Sprintf("(%d/%d)", b.cursor+1, len(v)))
	}
	switch {
	case b.mode == modeConfirm:
		foot = styWarn.Render("删除「" + oneLineTitle(b.target.Title(), 30) + "」？会删掉会话记录和图片，按 y 确认，其他键取消")
	case b.note != "" && b.noteOK:
		foot += "  " + styOK.Render(b.note)
	case b.note != "":
		foot += "  " + styWarn.Render(b.note)
	}
	if foot != "" {
		lines = append(lines, foot)
	}

	for i, l := range lines {
		lines[i] = ansi.Truncate(l, w, "…")
	}
	return strings.Join(lines, "\n")
}

// row 渲染一行：标题（起过名字的用青色）、可选的工作目录，右边是轮数和最近使用时间。
func (b *sessionBrowser) row(it SessionInfo, sel bool, now time.Time, w int) string {
	meta := fmt.Sprintf("%d 轮  %s", it.Turns, ago(now, it.Updated))
	if it.Current {
		meta = "当前 · " + meta
	}
	title := it.Title()
	if title == "" {
		title = "（无标题）"
	}
	extra := ""
	if b.showPath && it.Cwd != "" {
		extra = "  " + it.Cwd
	}
	// 标题区宽度 = 总宽 - 前缀 2 - 右侧信息 - 间隔 2
	avail := max(w-2-ansi.StringWidth(meta)-2, 4)
	text := ansi.Truncate(title+extra, avail, "…")
	pad := strings.Repeat(" ", max(w-2-ansi.StringWidth(text)-ansi.StringWidth(meta), 1))
	if sel {
		return styBrowseSel.Render("❯ " + text + pad + meta)
	}
	// 未选中时分段上色；截断按纯文本算好，这里只给已截断的文字加样式。
	tw := ansi.StringWidth(title)
	head, tail := ansi.Cut(text, 0, tw), ansi.Cut(text, tw, ansi.StringWidth(text))
	if it.Name != "" {
		head = styInfoThink.Render(head)
	}
	metaStyled := styFaint.Render(meta)
	if it.Current {
		metaStyled = styOK.Render(meta)
	}
	return "  " + head + styFaint.Render(tail) + pad + metaStyled
}

// spread 把 left、right 放在同一行两端；放不下时只留 left。
func spread(left, right string, w int) string {
	gap := w - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap < 2 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right
}

// ago 把时间显示成“3小时前”这样的相对时间，超过 30 天显示日期。
func ago(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d小时前", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%d天前", int(d.Hours()/24))
	}
	return t.Format("2006-01-02")
}

func oneLineTitle(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
