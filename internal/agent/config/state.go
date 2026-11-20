package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"

	"github.com/tailscale/hujson"
)

// Settings 是用户可以手改的运行设置，放在数据目录的 settings.jsonc。
//
// 刻意不写回 models.jsonc：那是用户手写的模型配置（带注释、有 default），
// 每次 /model、/effort 都去改它，会把“配置的意图”和“上次的选择”混在一起，
// 多开实例时还会互相覆盖。这里单独放一个小文件，坏了也不影响启动。
//
// 程序只在用户主动切换时改 model 与 effort 两个键（见 SaveSettings），
// 其余内容（注释、以后加进来的其他设置）原样保留，所以可以放心手改这个文件。
type Settings struct {
	// Model 是上次用的 "provider/model"；空表示没记录过。
	Model string `json:"model,omitempty"`
	// Effort 是上次用的档位；空表示没记录过。
	Effort string `json:"effort,omitempty"`
}

// SettingsPath 是设置文件的路径。
func SettingsPath(dataDir string) string { return joinDataDir(dataDir, "settings.jsonc") }

// SettingsTemplate 是首次运行时生成的 settings.jsonc。
// examples/settings.example.jsonc 必须与它逐字一致，由 template_test.go 保证。
const SettingsTemplate = `// Sprout 设置（JSONC：支持 // 和 /* */ 注释，以及尾逗号）。
// 改完之后重启 sprout 生效。
//
// model 与 effort 是上次用的模型与思考档位：/model、/effort 切换成功后会写回这里，
// 下次启动直接沿用。手改同样有效；记的模型或档位已经不存在时退回默认值并提示，不挡住启动。
// 命令行的 --effort 只对那一次生效，不会改这个文件。
{
  // 上次使用的模型，格式 "provider名/模型名"，两边都必须在 models.jsonc 里定义过。
  "model": "",
  // 上次使用的思考档位：off / low / medium / high / xhigh / max，或留空表示按模型默认。
  "effort": "",
}
`

// EnsureSettings 在 path 不存在或为空时写入模板，已有内容则原样保留。
//
// 空文件也要写：早期版本会在数据目录预建一批空的配置文件占位，空文件不是可用的配置，
// 而 LoadSettings 会把空内容当成语法错误。
func EnsureSettings(path string) (created bool, err error) {
	if info, statErr := os.Stat(path); statErr == nil && info.Size() > 0 {
		return false, nil
	} else if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
		return false, fmt.Errorf("无法检查 %s: %w", path, statErr)
	}
	if err := os.WriteFile(path, []byte(SettingsTemplate), 0o644); err != nil {
		return false, fmt.Errorf("无法写入 %s: %w", path, err)
	}
	return true, nil
}

// LoadSettings 读取 settings.jsonc。文件不存在或内容坏掉时返回零值和错误，由调用方决定退回默认值。
func LoadSettings(path string) (Settings, error) {
	var s Settings
	if err := decodeJSONC(path, &s); err != nil {
		return Settings{}, err
	}
	return s, nil
}

// SaveSettings 只更新 model 与 effort 两个键，文件里的注释和其他设置原样保留。
//
// 整份重写会丢掉用户手写的注释和以后加进来的设置，所以走按字节偏移替换的 editJSONC；
// 改完重新解析一遍，解析失败就回滚，不留下坏配置。文件还不存在时先补上模板再改。
func SaveSettings(path string, s Settings) error {
	if _, err := EnsureSettings(path); err != nil {
		return err
	}
	return editJSONC(path, "settings.jsonc", nil, func(text []byte, v *hujson.Value) ([]byte, error) {
		if !isJSONObject(v) {
			return nil, fmt.Errorf("settings.jsonc 的顶层不是对象")
		}
		text = setMember(text, v, "model", strconv.Quote(s.Model))
		// setMember 用的是调用时的偏移，改过一次后那些偏移已经对不上新文本，得重新解析。
		parsed, err := hujson.Parse(text)
		if err != nil {
			return nil, fmt.Errorf("改写 settings.jsonc 的 model 后无法再解析: %w", err)
		}
		return setMember(text, &parsed, "effort", strconv.Quote(s.Effort)), nil
	}, func(path string) error {
		_, err := LoadSettings(path)
		return err
	})
}

// IsSettingsMissing 报告 LoadSettings 的错误是不是“文件还不存在”。
func IsSettingsMissing(err error) bool { return errors.Is(err, fs.ErrNotExist) }

// MigrateStateFile 把旧的 state.json 搬到 settings.jsonc。
//
// state.json 是程序写的纯 JSON，没有注释，直接改名即可；settings.jsonc 已存在时不动它
// （用户可能已经手改过），旧文件留在原地。两边都不存在时什么也不做。
func MigrateStateFile(dataDir string) error {
	old := joinDataDir(dataDir, "state.json")
	dst := SettingsPath(dataDir)
	if _, err := os.Stat(old); err != nil {
		return nil
	}
	if _, err := os.Stat(dst); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("无法检查 %s: %w", dst, err)
	}
	if err := os.Rename(old, dst); err != nil {
		return fmt.Errorf("把 state.json 改名为 settings.jsonc 失败: %w", err)
	}
	return nil
}
