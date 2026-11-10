package agent

import (
	"strings"
	"testing"
)

// 真实 token 数由 DeepSeek V4 分词器数出（tokenizers 库，add_special_tokens=False）。文本是自拟的典型片段。
var tokenCases = []struct {
	name       string
	text       string
	truth, est int
}{
	{"中文回答", "好的，我先看一下当前目录的结构，再决定从哪里开始修改。这个问题的原因是配置文件里的路径写错了。", 23, 35},
	{"英文回答", "The build failed because the config file points to a missing directory. I will fix the path and rerun the tests.", 24, 29},
	{"中英混合", "我把 `ResolveDataDir` 改成了先检查 SPROUT_DATA_DIR 环境变量，再回退到 <exeDir>/data/.sprout。", 36, 33},
	{"思维链", "The user wants to list files. I should use PowerShell's Get-ChildItem with -Force to include hidden files, then summarize.", 27, 32},
	{"Go 代码", "func (m *Model) refresh() {\n\tif m.w <= 0 {\n\t\treturn\n\t}\n\tparts := make([]string, len(m.entries))\n\tfor i, e := range m.entries {\n\t\tparts[i] = e.render(m.w)\n\t}\n}\n", 57, 75},
	{"工具参数", `{"command":"Get-ChildItem -Path D:\\SmyProjects -Recurse -Filter *.go | Select-Object -First 20","timeout_seconds":60}`, 37, 46},
	{"命令输出", "    Directory: D:\\SmyProjects\\agent-source\\sprout\n\nMode                 LastWriteTime         Length Name\n----                 -------------         ------ ----\nd----          2026/11/10    14:39                bin\n-a---          2026/11/10    12:40           1705 build.cmd\n-a---          2026/11/09    11:36           3235 go.mod\n", 87, 114},
	{"go test 输出", "ok  \tgithub.com/shumengya/sprout/internal/agent\t0.538s\nok  \tgithub.com/shumengya/sprout/internal/tui\t0.950s\n--- FAIL: TestRender (0.00s)\n    render_test.go:42: got 12, want 13\n", 68, 72},
}

func TestEstimateTokensStaysCloseToRealTokenizer(t *testing.T) {
	for _, c := range tokenCases {
		got := EstimateTokens(c.text)
		if got != c.est {
			t.Errorf("%s: EstimateTokens = %d，期望 %d（规则改了的话，用真实分词器重新核对后再改这里）", c.name, got, c.est)
		}
		// 宁可高估：低估超过 15% 或高估超过 60% 都说明规则跑偏了。
		if r := float64(got) / float64(c.truth); r < 0.85 || r > 1.6 {
			t.Errorf("%s: 估算 %d ÷ 真实 %d = %.2f，超出 [0.85, 1.6]", c.name, got, c.truth, r)
		}
	}
}

func TestEstimateTokensByCharacterClass(t *testing.T) {
	for _, c := range []struct {
		text string
		want int
	}{
		{"", 0},
		{strings.Repeat("a", 300), 60},          // 字母连成一个“词”：每 5 个 1 个
		{strings.Repeat("ab ", 100), 100},       // 单个空格并进后面的词
		{"x" + strings.Repeat(" ", 8) + "y", 3}, // 缩进的连续空格另算 1 个
		{"1234567", 3},                          // 数字每 3 位 1 个
		{strings.Repeat("字", 300), 210},         // 汉字 0.7
		{strings.Repeat("，", 10), 10},           // 中文标点 1
		{strings.Repeat("─", 10), 15},           // 框线符号 1.5
		{strings.Repeat("😀", 10), 20},           // emoji 2
		{"a\r\nb", 3},                           // \r\n 只算一次换行
		{strings.Repeat("{}", 5), 8},            // ASCII 标点 0.8
		{"\xff\xfe", 3},                         // 非法 UTF-8 按替换字符（符号）计，不能 panic
	} {
		if got := EstimateTokens(c.text); got != c.want {
			t.Errorf("EstimateTokens(%q) = %d，期望 %d", c.text, got, c.want)
		}
	}
}

func BenchmarkEstimateTokens(b *testing.B) {
	s := strings.Repeat(tokenCases[6].text+tokenCases[0].text, 200)
	b.SetBytes(int64(len(s)))
	for range b.N {
		EstimateTokens(s)
	}
}
