package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/tailscale/hujson"
)

// SkillsTemplate 是 skills.jsonc 的初始内容。
//
// 技能必须注册在这里才会生效；注释写明字段含义、自动注册范围与
// 手动注册任意目录的写法，用户照着改就能用。
const SkillsTemplate = `{
  // 技能注册表：只有注册在这里的技能才会交给模型。
  "skills": {
    // 手动注册示例（去掉注释即可，path 指向含 SKILL.md 的目录，相对路径按数据目录解析）：
    // "pdf": {"path": "D:/work/my-skills/pdf", "enable": true}
  },

  // 是否自动加载 .agents/skills（项目里与用户主目录下的）并登记到上面的注册表。
  // 数据目录 skills/ 下的技能总是自动登记，不受这一项影响。设为 false 后，
  // 位于 .agents 目录下的技能一律不加载，注册表里的条目保留，改回 true 即可恢复。
  "scanAgents": true
}
`

// SkillEntry 是注册表里的一条技能。
type SkillEntry struct {
	// Path 是技能目录（含 SKILL.md 的那一层）；相对路径按数据目录解析。
	// 为空表示按名字在各扫描位置里找，只在旧配置里出现。
	Path string `json:"path"`
	// Enable 为 false 时技能不交给模型，但注册信息保留，面板里还能重新打开。
	Enable bool `json:"enable"`
}

// SkillsFile 对应 skills.jsonc。
type SkillsFile struct {
	// ScanAgents 控制是否加载 .agents/skills 下的技能；缺省视为开启。
	ScanAgents *bool                 `json:"scanAgents"`
	Skills     map[string]SkillEntry `json:"skills"`
	// Legacy 是旧版 "disabled": [...] 的残留，只在启动迁移时读一次。
	Legacy []string `json:"disabled"`
}

// SkillsPath 是 skills.jsonc 的路径。
func SkillsPath(dataDir string) string { return joinDataDir(dataDir, "skills.jsonc") }

// ScansAgents 报告是否加载 .agents/skills 下的技能。没写这一项时为 true：
// 引入开关之前的行为就是扫这些目录，老配置不该突然少掉技能。
func (f *SkillsFile) ScansAgents() bool { return f == nil || f.ScanAgents == nil || *f.ScanAgents }

// EnsureSkillsTemplate 在文件缺失或为空时写入模板，返回是否新建。
func EnsureSkillsTemplate(path string) (created bool, err error) {
	if info, statErr := os.Stat(path); statErr == nil && info.Size() > 0 {
		return false, nil
	} else if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
		return false, fmt.Errorf("检查技能配置失败: %w", statErr)
	}
	return true, os.WriteFile(path, []byte(SkillsTemplate), 0o600)
}

// LoadSkills 读取 skills.jsonc。文件不存在时视为还没有注册任何技能。
//
// 和 mcp.jsonc 一样，内容不合法时返回错误：注册表是技能的唯一入口，
// 静默当成空表会让所有技能一起消失，用户很难看出是配置写坏了。
func LoadSkills(path string) (*SkillsFile, error) {
	var f SkillsFile
	if err := decodeJSONC(path, &f); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &SkillsFile{}, nil
		}
		return nil, err
	}
	return &f, nil
}

// Names 返回注册的技能名，按名字排序。
func (f *SkillsFile) Names() []string {
	if f == nil {
		return nil
	}
	names := make([]string, 0, len(f.Skills))
	for name := range f.Skills {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Entry 返回某个技能的注册信息。
func (f *SkillsFile) Entry(name string) (SkillEntry, bool) {
	if f == nil {
		return SkillEntry{}, false
	}
	e, ok := f.Skills[name]
	return e, ok
}

// SetSkillEnabled 改写某个注册技能的 enable。技能必须已经在注册表里。
func SetSkillEnabled(path, name string, enable bool) error {
	return editJSONC(path, "skills.jsonc", []byte(SkillsTemplate), func(text []byte, v *hujson.Value) ([]byte, error) {
		obj := v.Find("/skills/" + jsonPointerEscape(name))
		if obj == nil || !isJSONObject(obj) {
			return nil, fmt.Errorf("skills.jsonc 里没有注册技能 %q", name)
		}
		return setMember(text, obj, "enable", boolLiteral(enable)), nil
	}, checkSkills)
}

// SetScanAgents 改写是否加载 .agents/skills 下的技能。
func SetScanAgents(path string, on bool) error {
	return editJSONC(path, "skills.jsonc", []byte(SkillsTemplate), func(text []byte, v *hujson.Value) ([]byte, error) {
		return setMember(text, v, "scanAgents", boolLiteral(on)), nil
	}, checkSkills)
}

// AddSkill 把技能 name 注册进表里，dir 是它的技能目录；已注册就不动。
// 返回是否真的写入。
func AddSkill(path, name, dir string) (bool, error) {
	f, err := LoadSkills(path)
	if err != nil {
		return false, err
	}
	if _, ok := f.Skills[name]; ok {
		return false, nil
	}
	if dir == "" {
		return false, errors.New("注册技能需要目录路径")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false, err
	}
	entry := fmt.Sprintf(`{"path": %s, "enable": true}`, strconv.Quote(filepath.ToSlash(abs)))
	if err := editJSONC(path, "skills.jsonc", []byte(SkillsTemplate), func(text []byte, v *hujson.Value) ([]byte, error) {
		return addSkillMember(text, v, name, entry)
	}, checkSkills); err != nil {
		return false, err
	}
	return true, nil
}

// addSkillMember 在 /skills 下插入一条技能。没有 skills 对象时先补一个空对象。
//
// 每次改完都重新解析，后续插入的字节偏移才不会失效。
func addSkillMember(text []byte, v *hujson.Value, name, entry string) ([]byte, error) {
	obj := v.Find("/skills")
	if obj == nil || !isJSONObject(obj) {
		text = setMember(text, v, "skills", "{}")
		parsed, err := hujson.Parse(text)
		if err != nil {
			return nil, err
		}
		v = &parsed
		obj = v.Find("/skills")
		if obj == nil {
			return nil, errors.New("skills.jsonc 里的 skills 不是对象")
		}
	}
	return setMember(text, obj, name, entry), nil
}

// WriteSkills 按注册表重写整个 skills.jsonc，只用于旧版 disabled 列表的一次性迁移。
// 平时的改动都走单条就地改写，保留用户的注释与排版。
func WriteSkills(path string, f *SkillsFile) error {
	var b strings.Builder
	b.WriteString("{\n")
	b.WriteString("  // 技能注册表：只有注册在这里的技能才会交给模型。\n")
	b.WriteString("  \"skills\": {")
	for i, name := range f.Names() {
		e := f.Skills[name]
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "\n    %s: {\"path\": %s, \"enable\": %s}",
			strconv.Quote(name), strconv.Quote(e.Path), boolLiteral(e.Enable))
	}
	if len(f.Names()) > 0 {
		b.WriteString("\n  ")
	}
	b.WriteString("},\n  \"scanAgents\": " + boolLiteral(f.ScansAgents()) + "\n}\n")
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

// checkSkills 校验写回后的文件仍能按注册表解析。
func checkSkills(path string) error {
	_, err := LoadSkills(path)
	return err
}
