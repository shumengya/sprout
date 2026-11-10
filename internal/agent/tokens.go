package agent

import (
	"unicode"
	"unicode/utf8"
)

// EstimateTokens 估算一段文本的 token 数，在拿不到服务端用量时用（上下文预算、思维链 token 数）。
//
// 规则按字符类别计：英文字母连成的单词每 5 个字母约 1 个 token，数字每 3 位 1 个，ASCII 标点约 0.8 个，
// 单个空格并进后面的词、连续空格（缩进）约 1 个；汉字约 0.7 个，中文标点 1 个，其他文字 1 个，
// 框线、箭头等符号 1.5 个，emoji 等增补平面字符 2 个。
//
// 系数用 DeepSeek V4 分词器在真实会话（对话、思维链、工具参数、命令输出）上校准，宁可略微偏高：
// 高估只会让压缩早一点发生，低估才会撞上窗口。对照结果（估算 ÷ 真实，留出的检验集）：
//
//	                     总量    中位    P10    P90   低估超过 20% 的片段
//	DeepSeek V4 / GLM-5  1.10   1.18   0.92   1.37   1%
//	OpenAI o200k         1.05   1.11   0.91   1.28   1%
//	字符数 ÷ 4（常见做法） 0.65   0.62   0.42   0.89   81%
//
// 字符数 ÷ 4 是按英文散文定的，代码、JSON、命令输出的 token 密度高得多，在 agent 的上下文里明显低估。
// 已知偏差：旧的 OpenAI cl100k 分词器（gpt-4、gpt-3.5）每个汉字约 1 个 token，中文多时会低估约三成；
// 内核用服务端上次报告的真实用量做基准，只对之后新增的消息估算，所以影响有限。
func EstimateTokens(s string) int {
	// 以 0.1 token 为单位累计，避免浮点误差，最后向上取整。
	t, letters, digits, spaces := 0, 0, 0, 0
	flush := func() {
		t += 10 * ((letters+4)/5 + (digits+2)/3)
		if spaces >= 2 {
			t += 10
		}
		letters, digits, spaces = 0, 0, 0
	}
	for i := 0; i < len(s); {
		r, size := rune(s[i]), 1
		if r >= utf8.RuneSelf {
			r, size = utf8.DecodeRuneInString(s[i:])
		}
		i += size
		switch {
		case r < utf8.RuneSelf && isASCIILetter(r):
			if digits > 0 || spaces > 0 {
				flush()
			}
			letters++
			continue
		case r >= '0' && r <= '9':
			if letters > 0 || spaces > 0 {
				flush()
			}
			digits++
			continue
		case r == ' ':
			if letters > 0 || digits > 0 {
				flush()
			}
			spaces++
			continue
		}
		flush()
		t += runeCost(r)
	}
	flush()
	return (t + 9) / 10
}

func isASCIILetter(r rune) bool { return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' }

// runeCost 是单个字符的开销（0.1 token 为单位）。字母、数字、空格不走这里，它们按连续的一段计。
func runeCost(r rune) int {
	switch {
	case r == '\n':
		return 10
	case r == '\r': // \r\n 在分词器里通常是一个 token，算在 \n 上
		return 0
	case r == '\t':
		return 5
	case r < utf8.RuneSelf: // ASCII 标点、符号
		return 8
	case isHan(r):
		return 7
	case r >= 0x3000 && r <= 0x303F || r >= 0xFF00 && r <= 0xFFEF: // 中文标点、全角字符
		return 10
	case unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsNumber(r): // 其他文字：假名、韩文、带重音的字母等
		return 10
	case r > 0xFFFF: // emoji 等增补平面字符
		return 20
	}
	return 15 // 框线、箭头、数学符号等
}

func isHan(r rune) bool {
	return r >= 0x4E00 && r <= 0x9FFF || r >= 0x3400 && r <= 0x4DBF ||
		r >= 0xF900 && r <= 0xFAFF || r >= 0x20000 && r <= 0x3FFFF
}
