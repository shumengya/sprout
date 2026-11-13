package tui

import (
	"encoding/json"
	"strings"
)

// skillFor 判断这次工具调用是不是在读某个技能的文件，返回技能名（不是则返回空串）。
//
// 技能正文约定由模型用 read 工具按 location 读取，但模型也可能用 bash / powershell
// 的 cat、Get-Content 去读，所以不能只看工具名：凡是参数里出现某个技能的 SKILL.md，
// 或该技能目录下的任何文件（正文里引用的 scripts/、references/），都算命中。
// 只匹配已安装技能的路径，别的文件读得再多也不会误标。
func (m *Model) skillFor(args string) string {
	path := pathInArgs(args)
	if path == "" {
		return ""
	}
	got := normalizePath(path)
	if got == "" {
		return ""
	}
	for _, sk := range m.opts.Info.Skills {
		if sk.Path == "" {
			continue
		}
		if sameFile(got, normalizePath(sk.Path)) {
			return sk.Name
		}
		// 技能目录下的资源文件也算：模型读 references/ 说明它正在按这个技能办事。
		if dir := skillDir(sk.Path); dir != "" && strings.Contains(got, dir) {
			return sk.Name
		}
	}
	return ""
}

// skillDir 返回 SKILL.md 所在的目录，统一成 normalizePath 的形式并带上结尾斜杠，
// 这样按前缀匹配时不会把 lark 和 lark-drive 混为一谈。
func skillDir(skillPath string) string {
	p := normalizePath(skillPath)
	i := strings.LastIndex(p, "/")
	if i <= 0 {
		return ""
	}
	return p[:i+1]
}

// pathInArgs 取出工具参数里可能与文件路径有关的字符串：read / write 的 path，
// shell 工具的 command。取不到就返回空串。
//
// 这里不解 JSON 之外的语法，也不追求精确：只用来做个提示标记，宁可漏判也不要错标。
func pathInArgs(args string) string {
	var a struct {
		Path    string `json:"path"`
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(args), &a) != nil {
		return ""
	}
	if strings.TrimSpace(a.Path) != "" {
		return a.Path
	}
	return a.Command
}

// sameFile 比较两个已归一化的路径是否同一个文件。命令里取出来的是整行命令，
// 所以不要求整串相等：包含即可。
func sameFile(got, want string) bool {
	if got == "" || want == "" {
		return false
	}
	return got == want || strings.Contains(got, want)
}

// normalizePath 统一成小写、正斜杠，并去掉首尾空白与包裹的引号。
// 反斜杠一律换成正斜杠（不用 filepath.ToSlash：它在非 Windows 上不动反斜杠），
// 再把连续的斜杠合并：bash 命令里的 Windows 路径常被写成 C:\\Users\\...。
func normalizePath(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'`)
	s = strings.ReplaceAll(s, `\`, "/")
	for strings.Contains(s, "//") {
		s = strings.ReplaceAll(s, "//", "/")
	}
	return strings.ToLower(s)
}
