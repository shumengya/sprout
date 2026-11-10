package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// mdFlow 把带样式的文本折成不超过 w 列的若干行，并让每一行的样式自成一体（见 mdSeal）。
func mdFlow(s string, w int) []string {
	return mdSeal(strings.Split(wrap(s, w), "\n"))
}

// mdSeal 让每一行的 SGR 样式自成一体：折行可能把一个样式区间切成两半，
// 而 viewport 会从任意一行开始显示、前缀（引用条、缩进）也会插在行首，
// 所以每行都要在行首补上上一行遗留的样式，并在行尾显式复位。
func mdSeal(lines []string) []string {
	var active []string
	for i, l := range lines {
		if l == "" {
			continue
		}
		carry := strings.Join(active, "")
		active = mdTrack(l, active)
		if carry != "" {
			l = carry + l
		}
		if len(active) > 0 {
			l += "\x1b[m"
		}
		lines[i] = l
	}
	return lines
}

// mdTrack 扫描 line 里的 SGR 序列，返回行尾仍然生效的那些序列。
func mdTrack(line string, active []string) []string {
	for i := 0; i < len(line); {
		if line[i] != 0x1b {
			i++
			continue
		}
		n := mdEscLen(line[i:])
		seq := line[i : i+n]
		i += n
		if len(seq) < 3 || seq[1] != '[' || seq[len(seq)-1] != 'm' {
			continue // 只关心 SGR；其他序列（如超链接）不跨行追踪
		}
		if params := seq[2 : len(seq)-1]; params == "" || params == "0" {
			active = active[:0]
		} else {
			active = append(active, seq)
		}
	}
	return active
}

// mdEscLen 返回 s 开头的转义序列长度（s[0] 必为 ESC）。
func mdEscLen(s string) int {
	if len(s) < 2 {
		return len(s)
	}
	switch s[1] {
	case '[': // CSI：结束字节 0x40-0x7e
		for i := 2; i < len(s); i++ {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1
			}
		}
	case ']': // OSC：以 BEL 或 ESC \ 结束
		for i := 2; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
	}
	return 2
}

// mdPrefix 给每行加前缀：第一行用 first，其余用 rest。空行不留尾随空格（引用条这类非空白前缀会保留）。
func mdPrefix(lines []string, first, rest string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		p := rest
		if i == 0 {
			p = first
		}
		out[i] = p + l
		if l == "" {
			out[i] = strings.TrimRight(out[i], " ")
		}
	}
	return out
}

// mdClamp 兜底：任何一行超宽都截断，保证渲染结果绝不撑破布局。
func mdClamp(lines []string, w int) {
	for i, l := range lines {
		if ansi.StringWidth(l) > w {
			lines[i] = ansi.Truncate(l, w, "")
		}
	}
}

func mdRule(w int) string {
	return mdFaint.Render(strings.Repeat("─", min(w, 40)))
}
