package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/tailscale/hujson"
)

// skillsSeed 是 skills.jsonc 第一次写入时的内容：仍是全开状态。
// 注释说明开关的作用范围，免得用户以为停用会动到技能文件。
const skillsSeed = `{
  // 技能开关：/skills 面板写回这里。只列出被停用的技能名；
  // 技能文件本身不受影响，重新启用后下一轮对话即可用。
  "disabled": []
}
`

// SkillState 对应 skills.jsonc。技能来自磁盘上的目录，没有别的配置入口，
// 所以这里只记录“哪些技能不告诉模型”这一件事。
type SkillState struct {
	Disabled []string `json:"disabled"`
}

// SkillsStatePath 是 skills.jsonc 的路径。
func SkillsStatePath(dataDir string) string { return joinDataDir(dataDir, "skills.jsonc") }

// LoadSkillState 读取 skills.jsonc。文件不存在时视为没有停用任何技能，
// 返回空状态而不是错误：技能开关是可选的，不该因为缺文件而让 sprout 起不来。
func LoadSkillState(path string) (*SkillState, error) {
	var s SkillState
	if err := decodeJSONC(path, &s); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &SkillState{}, nil
		}
		return nil, err
	}
	return &s, nil
}

// IsDisabled 报告某个技能是否被停用。
func (s *SkillState) IsDisabled(name string) bool {
	if s == nil {
		return false
	}
	for _, n := range s.Disabled {
		if strings.EqualFold(strings.TrimSpace(n), name) {
			return true
		}
	}
	return false
}

// SetSkillDisabled 把技能 name 加入或移出停用列表，文件不存在时先按模板建出来。
// 名字按大小写不敏感比较：已有的同类项会被合并，不会越写越多。
func SetSkillDisabled(path, name string, disabled bool) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("技能名不能为空")
	}
	// 先按语义读一遍：文件写坏了在这里就报错，不会被改写覆盖。缺文件时是空状态。
	st, err := LoadSkillState(path)
	if err != nil {
		return err
	}
	return editJSONC(path, "skills.jsonc", []byte(skillsSeed), func(text []byte, v *hujson.Value) ([]byte, error) {
		list := make([]string, 0, len(st.Disabled)+1)
		for _, n := range st.Disabled {
			if !strings.EqualFold(strings.TrimSpace(n), name) {
				list = append(list, n)
			}
		}
		if disabled {
			list = append(list, name)
		}
		raw, err := json.Marshal(list)
		if err != nil {
			return nil, fmt.Errorf("序列化技能开关失败: %w", err)
		}
		return setMember(text, v, "disabled", string(raw)), nil
	}, nil)
}
