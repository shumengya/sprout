// Package skills 实现 Agent Skills：发现含 SKILL.md 的目录、解析 frontmatter，
// 并把技能目录按渐进式披露接进系统提示词。
//
// 只负责“有哪些技能、各自是什么”；技能正文由模型在需要时用 read 工具读取，
// 不在这里预加载——这正是渐进式披露的意义。
package skills

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// FileName 是技能入口文件名，必须逐字匹配。
const FileName = "SKILL.md"

// MaxDescription 是 description 的规范上限（字符数），超出时截断并在提示里说明。
const MaxDescription = 1024

const (
	// maxDepth 是相对扫描根向下最多找几层，避免在大仓库里跑飞。
	maxDepth = 4
	// maxDirs 是一次扫描最多看多少个目录。
	maxDirs = 2000
	// maxFileBytes 是单个 SKILL.md 的读取上限，防止误放的大文件撑爆内存。
	maxFileBytes = 1 << 20
)

// Source 是技能的来源范围，用于优先级与展示。
type Source string

const (
	SourceProject Source = "项目"
	SourceUser    Source = "用户"
	SourceExtra   Source = "自定义"
)

// Root 是一处技能目录（其下每个含 SKILL.md 的子目录算一个技能）。
// Roots 的顺序即优先级：靠前的先被采用，同名技能被后面的遮蔽时会给出提示。
type Root struct {
	Dir    string
	Source Source
}

// Skill 是一个已发现的技能的元数据。正文不在这里，运行时按需读取。
type Skill struct {
	Name        string
	Description string
	License     string
	Compat      string
	Allowed     string
	Metadata    map[string]string
	// Path 是 SKILL.md 的绝对路径；Dir 是技能目录的绝对路径。
	Path string
	Dir  string
	// digest 是 SKILL.md 的内容摘要，用来识别不同目录里的同一份技能。
	digest [32]byte
	// Source 表示来自项目、用户还是自定义目录。
	Source Source
}

// Discover 按 Roots 的顺序扫描技能，返回按名字排序的结果和所有需要告知用户的问题。
//
// 同名技能先到先得（项目级通常排在用户级前面），后出现的记一条遮蔽提示。
// 单个技能解析失败只跳过它，不影响其余技能。
func Discover(roots []Root) (list []Skill, problems []string) {
	byName := map[string]Skill{}
	for _, root := range roots {
		for _, dir := range scan(root.Dir, &problems) {
			sk, warns, err := Load(dir, root.Source)
			problems = append(problems, warns...)
			if err != nil {
				problems = append(problems, fmt.Sprintf("技能 %s 未加载：%v", dir, err))
				continue
			}
			if prev, ok := byName[sk.Name]; ok {
				// 同一份技能在多个位置出现（比如 .agents/skills 与 .claude/skills 互为副本）是常态，
				// 内容一致就静默跳过，只有内容真的不同才提示用户有一份被遮蔽了。
				if prev.digest == sk.digest {
					continue
				}
				problems = append(problems, fmt.Sprintf("技能 %q 被 %s 遮蔽（同名技能 %s 未加载）", sk.Name, prev.Path, sk.Path))
				continue
			}
			byName[sk.Name] = sk
		}
	}
	for _, sk := range byName {
		list = append(list, sk)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, problems
}

// scan 在 root 下找技能目录。找到含 SKILL.md 的目录后不再向下深入：
// 技能自带的 scripts / references 不该被当成另一个技能。
func scan(root string, problems *[]string) []string {
	root = filepath.Clean(root)
	info, err := os.Stat(root)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil // 没装技能的目录是常态，不打扰用户
	case err != nil:
		*problems = append(*problems, fmt.Sprintf("无法访问技能目录 %s：%v", root, err))
		return nil
	case !info.IsDir():
		*problems = append(*problems, fmt.Sprintf("技能目录 %s 不是目录，已跳过", root))
		return nil
	}

	type item struct {
		dir   string
		depth int
	}
	var found []string
	seen := map[string]bool{}
	queue := []item{{root, 0}}
	scanned := 0
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		key := strings.ToLower(filepath.Clean(it.dir)) // Windows 路径不区分大小写，符号链接也按同一目录算
		if seen[key] {
			continue
		}
		seen[key] = true
		if scanned >= maxDirs {
			*problems = append(*problems, fmt.Sprintf("技能目录 %s 下的目录超过 %d 个，只扫描了一部分", root, maxDirs))
			break
		}
		scanned++

		if isSkillDir(it.dir) {
			found = append(found, it.dir)
			continue
		}
		if it.depth >= maxDepth {
			continue
		}
		entries, err := os.ReadDir(it.dir)
		if err != nil {
			if it.dir == root {
				*problems = append(*problems, fmt.Sprintf("无法读取技能目录 %s：%v", root, err))
			}
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if !e.IsDir() || name == ".git" || name == "node_modules" || strings.HasPrefix(name, ".") {
				continue
			}
			queue = append(queue, item{filepath.Join(it.dir, name), it.depth + 1})
		}
	}
	sort.Strings(found)
	return found
}

// isSkillDir 判断目录里是否有名为 SKILL.md 的普通文件。
func isSkillDir(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, FileName))
	return err == nil && info.Mode().IsRegular()
}

// Load 解析一个技能目录。返回的 warnings 是需要告诉用户但不影响加载的问题。
func Load(dir string, source Source) (Skill, []string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Skill{}, nil, err
	}
	path := filepath.Join(abs, FileName)
	data, err := readLimited(path, maxFileBytes)
	if err != nil {
		return Skill{}, nil, err
	}
	fm, err := splitFrontmatter(data)
	if err != nil {
		return Skill{}, nil, err
	}

	var warnings []string
	front, yamlErr := parseFrontmatter(fm)
	if yamlErr != nil {
		// 其它客户端写出的技能常有未加引号的冒号，严格 YAML 会拒绝。
		// 这里退一步逐行取 name / description，尽量把技能用起来。
		front = lenientFrontmatter(fm)
		if front.Description == "" {
			return Skill{}, nil, fmt.Errorf("frontmatter 不是有效的 YAML（%v），也读不出 description", yamlErr)
		}
		warnings = append(warnings, fmt.Sprintf("技能 %s 的 frontmatter 不是严格合法的 YAML，已按宽松模式读取：%v", path, yamlErr))
	}

	sk := Skill{
		Name:        strings.TrimSpace(front.Name),
		Description: strings.Join(strings.Fields(front.Description), " "), // 折成一行，目录更紧凑
		License:     strings.TrimSpace(front.License),
		Compat:      strings.TrimSpace(front.Compatibility),
		Allowed:     strings.TrimSpace(front.AllowedTools),
		Metadata:    toStringMap(front.Metadata),
		Path:        path,
		Dir:         abs,
		digest:      sha256.Sum256(data),
		Source:      source,
	}

	base := filepath.Base(abs)
	if sk.Name == "" {
		sk.Name = base
		warnings = append(warnings, fmt.Sprintf("技能 %s 没有 name，暂用目录名 %q", path, base))
	} else if sk.Name != base {
		warnings = append(warnings, fmt.Sprintf("技能 %s 的 name %q 与目录名 %q 不一致", path, sk.Name, base))
	}
	if !namePattern.MatchString(sk.Name) || len(sk.Name) > 64 {
		warnings = append(warnings, fmt.Sprintf("技能 %s 的 name %q 不符合规范（只用小写字母、数字和单个连字符，最长 64）", path, sk.Name))
	}
	if sk.Description == "" {
		return Skill{}, warnings, errors.New("description 为空：模型靠它判断什么时候该用这个技能")
	}
	if utf8.RuneCountInString(sk.Description) > MaxDescription {
		r := []rune(sk.Description)
		sk.Description = string(r[:MaxDescription])
		warnings = append(warnings, fmt.Sprintf("技能 %s 的 description 超过 %d 字符，已截断", path, MaxDescription))
	}
	return sk, warnings, nil
}

var namePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// readLimited 读取整个文件，超过 limit 时返回错误。
func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s 超过 %d KB，太大，不当作文本技能读取", path, limit/1024)
	}
	return data, nil
}

// splitFrontmatter 切出 --- 之间的 YAML。文件必须以 frontmatter 开头。
func splitFrontmatter(data []byte) ([]byte, error) {
	text := strings.TrimPrefix(string(data), "\uFEFF")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], "\r") != "---" {
		return nil, errors.New("缺少 YAML frontmatter：文件必须以 --- 开头")
	}
	for i := 1; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], "\r")
		if line == "---" || line == "..." {
			return []byte(strings.Join(lines[1:i], "\n")), nil
		}
	}
	return nil, errors.New("YAML frontmatter 没有结束的 ---")
}

// front 是 frontmatter 里用到的字段。未知字段由 yaml 默认忽略。
type front struct {
	Name          string         `yaml:"name"`
	Description   string         `yaml:"description"`
	License       string         `yaml:"license"`
	Compatibility string         `yaml:"compatibility"`
	Metadata      map[string]any `yaml:"metadata"`
	AllowedTools  string         `yaml:"allowed-tools"`
}

func parseFrontmatter(fm []byte) (front, error) {
	var f front
	err := yaml.Unmarshal(fm, &f)
	return f, err
}

// lenientFrontmatter 在严格解析失败时逐行提取顶层 name / description，
// 支持 `key: value` 与 `key: >` / `key: |` 块标量。
func lenientFrontmatter(fm []byte) front {
	var f front
	lines := strings.Split(string(fm), "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], "\r")
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "#") {
			continue // 缩进的是块标量内容或子字段，由外层处理
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key != "name" && key != "description" {
			continue
		}
		value = strings.TrimSpace(value)
		switch value {
		case ">", "|", ">-", "|-", ">+", "|+":
			var block []string
			for i+1 < len(lines) && (strings.HasPrefix(lines[i+1], " ") || strings.TrimSpace(lines[i+1]) == "") {
				i++
				block = append(block, strings.TrimSpace(lines[i]))
			}
			value = strings.TrimSpace(strings.Join(block, " "))
		default:
			value = unquote(value)
		}
		if key == "name" {
			f.Name = value
		} else {
			f.Description = value
		}
	}
	return f
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func toStringMap(m map[string]any) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = fmt.Sprint(v)
	}
	return out
}

// AppendCatalog 把技能目录追加到系统提示词末尾。
//
// 和 AGENTS.md 一样追加而不是做成模板变量：用户的 SYSTEMPROMPT.md 可能是旧版本或完全自写的，
// 做成变量的话没写它的模板会让技能悄无声息地失效。
//
// hasRead 表示当前注册了 read 工具：没有它时改成用 shell 打开文件。
func AppendCatalog(system string, list []Skill, hasRead bool) string {
	if len(list) == 0 {
		return system
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(system, "\n"))
	b.WriteString("\n\n## 可用技能\n")
	b.WriteString("下面是本机安装的技能。技能是“用到才翻的说明书”，不是每次都要遵守的规则：\n")
	if hasRead {
		b.WriteString("任务与某个技能的 description 相符时，先用 read 工具读取它的 location，再按正文里的步骤做。\n")
	} else {
		b.WriteString("任务与某个技能的 description 相符时，先用 bash / powershell 打开它的 location（当前没有 read 工具），再按正文里的步骤做。\n")
	}
	b.WriteString("技能正文里引用的相对路径（scripts/…、references/…）相对技能目录解析，调用工具时请拼成绝对路径。\n")
	b.WriteString("不要凭空猜技能内容，也不要为了“看看有什么”把全部技能都读一遍。\n\n")
	b.WriteString("<available_skills>\n")
	for _, sk := range list {
		b.WriteString("  <skill>\n")
		fmt.Fprintf(&b, "    <name>%s</name>\n", html.EscapeString(sk.Name))
		fmt.Fprintf(&b, "    <description>%s</description>\n", html.EscapeString(sk.Description))
		fmt.Fprintf(&b, "    <location>%s</location>\n", html.EscapeString(sk.Path))
		b.WriteString("  </skill>\n")
	}
	b.WriteString("</available_skills>\n")
	return b.String()
}
