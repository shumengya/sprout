// Package subagent 实现子代理：主代理通过 agent 工具把一块独立的工作交给一个新的 agent，
// 子代理用自己的上下文做完，只把一份报告交回来。
//
// 本包只管“有哪些子代理类型”和“怎么跑一次”；子代理用哪个模型、怎么拼系统提示词、
// 记录写到哪里，由调用方通过 Spawner 决定（cmd/sprout 负责装配）。
package subagent

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Source 是子代理定义的来源。
type Source string

const (
	SourceBuiltin Source = "内置"
	SourceUser    Source = "用户"
	SourceProject Source = "项目"
)

// Definition 是一种子代理。
type Definition struct {
	Name        string
	Description string // 给主代理看：什么时候派这种子代理
	// Prompt 是角色说明，追加在子代理系统提示词的末尾；可以为空。
	Prompt string
	// Tools 是允许子代理使用的工具名，支持 path.Match 通配（如 mcp__github__*）；
	// nil 表示主代理的全部工具。agent 工具本身永远不给子代理：只允许一层。
	Tools []string
	// Model 是 models.jsonc 里的 provider/model 引用；空表示沿用主代理当前的模型。
	Model string
	// Effort 是思考档位；空表示沿用（同一个模型时跟主代理一致，换了模型时用该模型的默认档位）。
	Effort string
	Source Source
	Path   string // 定义文件的绝对路径；内置的为空
}

// Allows 报告名为 name 的工具能否交给这种子代理。
func (d Definition) Allows(name string) bool {
	if d.Tools == nil {
		return true
	}
	for _, p := range d.Tools {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

// DefaultAgent 是 agent 参数省略时使用的类型。
const DefaultAgent = "general"

// Builtins 返回内置的子代理类型。每次返回新切片，调用方可以随意修改。
func Builtins() []Definition {
	return []Definition{
		{
			Name: DefaultAgent,
			Description: "通用子代理，能用主代理的全部工具（含执行命令和改文件）。" +
				"适合把一块边界清楚、能独立完成的工作整个交出去：实现一个小功能、跑一组测试并修好失败项、在多处代码里调研后汇总。",
			Prompt: "按任务说明把工作做完，再交回报告。改动范围以任务说明为准，不要扩大到说明之外的文件。",
			Source: SourceBuiltin,
		},
		{
			Name: "explore",
			Description: "只读调查子代理，只有 read 和 shell 工具。用来找文件、读代码、梳理调用关系，" +
				"回答“X 在哪里、怎么工作”这类问题，不改任何文件。派出几个分别查不同的部分，能省下主对话的上下文。",
			Tools: []string{"read", "bash", "powershell"},
			Prompt: "这是只读任务：不要创建、修改、删除、移动任何文件，也不要运行会改变系统状态的命令" +
				"（安装依赖、git commit / checkout、写重定向 > 或 >> 等）。shell 只用来列目录、搜索和查看内容。\n" +
				"互相独立的查找放在同一次回复里并行发出。报告里给出具体的 路径:行号，让主代理不用再找一遍。",
			Source: SourceBuiltin,
		},
	}
}

// Root 是一处子代理定义目录：其下每个 *.md 文件是一种子代理。
type Root struct {
	Dir    string
	Source Source
}

const maxFileBytes = 256 << 10

var namePattern = regexp.MustCompile(`^[a-z0-9]+([_-][a-z0-9]+)*$`)

// Load 返回内置类型加上 roots 里定义的类型，按名字排序。
//
// roots 按优先级从低到高排列：后面的同名定义覆盖前面的，内置类型优先级最低，
// 所以用户可以写一个 explore.md 换掉内置的 explore（比如让它用更便宜的模型）。
// 单个文件解析失败只跳过它，原因写进 problems。
func Load(roots []Root) (list []Definition, problems []string) {
	byName := map[string]Definition{}
	for _, d := range Builtins() {
		byName[d.Name] = d
	}
	for _, root := range roots {
		for _, p := range scan(root.Dir, &problems) {
			d, err := loadFile(p, root.Source)
			if err != nil {
				problems = append(problems, fmt.Sprintf("子代理定义 %s 未加载：%v", p, err))
				continue
			}
			byName[d.Name] = d
		}
	}
	for _, d := range byName {
		list = append(list, d)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, problems
}

// scan 列出目录下的 *.md 文件（不递归）。目录不存在是常态，不算问题。
func scan(dir string, problems *[]string) []string {
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		*problems = append(*problems, fmt.Sprintf("无法读取子代理目录 %s：%v", dir, err))
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		// README 是给人看的说明，不是定义；别为它报一条“缺少 frontmatter”。
		if e.IsDir() || !strings.EqualFold(filepath.Ext(name), ".md") || strings.EqualFold(name, "README.md") {
			continue
		}
		out = append(out, filepath.Join(dir, name))
	}
	sort.Strings(out)
	return out
}

// front 是定义文件的 frontmatter。tools 既可以写成 YAML 列表，也可以写成逗号分隔的一行。
type front struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Tools       any    `yaml:"tools"`
	Model       string `yaml:"model"`
	Effort      string `yaml:"effort"`
}

func loadFile(p string, source Source) (Definition, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return Definition{}, err
	}
	data, err := readLimited(abs)
	if err != nil {
		return Definition{}, err
	}
	fm, body, err := splitFrontmatter(string(data))
	if err != nil {
		return Definition{}, err
	}
	var f front
	if err := yaml.Unmarshal([]byte(fm), &f); err != nil {
		return Definition{}, fmt.Errorf("frontmatter 不是有效的 YAML：%v", err)
	}

	d := Definition{
		Name:        strings.TrimSpace(f.Name),
		Description: strings.Join(strings.Fields(f.Description), " "),
		Prompt:      strings.TrimSpace(body),
		Model:       strings.TrimSpace(f.Model),
		Effort:      strings.ToLower(strings.TrimSpace(f.Effort)),
		Source:      source,
		Path:        abs,
	}
	if d.Name == "" {
		d.Name = strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
	}
	if !namePattern.MatchString(d.Name) || len(d.Name) > 64 {
		return Definition{}, fmt.Errorf("name %q 不合法：只用小写字母、数字，以及单个 - 或 _ 连接，最长 64", d.Name)
	}
	if d.Description == "" {
		return Definition{}, errors.New("description 为空：主代理靠它判断什么时候派这种子代理")
	}
	if d.Model == "inherit" { // 其他客户端的写法，等同于不写
		d.Model = ""
	}
	if d.Tools, err = toolList(f.Tools); err != nil {
		return Definition{}, err
	}
	return d, nil
}

// toolList 解析 tools 字段。省略返回 nil（全部工具）；写了但为空表示一个工具都不给。
func toolList(v any) ([]string, error) {
	var raw []string
	switch t := v.(type) {
	case nil:
		return nil, nil
	case string:
		raw = strings.Split(t, ",")
	case []any:
		for _, x := range t {
			s, ok := x.(string)
			if !ok {
				return nil, fmt.Errorf("tools 里的 %v 不是字符串", x)
			}
			raw = append(raw, s)
		}
	default:
		return nil, errors.New("tools 要写成列表或逗号分隔的字符串")
	}
	out := []string{}
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, err := path.Match(s, ""); err != nil {
			return nil, fmt.Errorf("tools 里的 %q 不是合法的通配模式", s)
		}
		out = append(out, s)
	}
	return out, nil
}

func readLimited(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileBytes {
		return nil, fmt.Errorf("文件超过 %d KB", maxFileBytes>>10)
	}
	return data, nil
}

// splitFrontmatter 切出开头 --- 之间的 YAML 和其后的正文。
func splitFrontmatter(text string) (fm, body string, err error) {
	text = strings.TrimPrefix(text, "\xef\xbb\xbf") // UTF-8 BOM
	lines := strings.Split(text, "\n")
	if strings.TrimRight(lines[0], "\r") != "---" {
		return "", "", errors.New("缺少 YAML frontmatter：文件必须以 --- 开头")
	}
	for i := 1; i < len(lines); i++ {
		if l := strings.TrimRight(lines[i], "\r"); l == "---" || l == "..." {
			return strings.Join(lines[1:i], "\n"), strings.Join(lines[i+1:], "\n"), nil
		}
	}
	return "", "", errors.New("YAML frontmatter 没有结束的 ---")
}
