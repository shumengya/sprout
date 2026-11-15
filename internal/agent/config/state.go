package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// State 是跨进程保留的界面选择：上次用的模型和思考档位。
//
// 刻意不写回 models.jsonc：那是用户手写的配置（带注释、有 default），
// 每次 /model、/effort 都去改它，会把“配置的意图”和“上次的选择”混在一起，
// 多开实例时还会互相覆盖。这里单独放一个小文件，坏了也不影响启动。
type State struct {
	// Model 是上次用的 "provider/model"；空表示没记录过。
	Model string `json:"model,omitempty"`
	// Effort 是上次用的档位；空表示没记录过。
	Effort string `json:"effort,omitempty"`
}

// StatePath 是界面选择记录文件的路径。
func StatePath(dataDir string) string { return joinDataDir(dataDir, "state.json") }

// LoadState 读取 state.json。文件不存在或内容坏掉时返回零值和错误，由调用方决定退回默认值。
func LoadState(path string) (State, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	return s, nil
}

// SaveState 原子写入 state.json：先写同目录临时文件再改名，中途失败不会留下半截内容。
func SaveState(path string, s State) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("无法写入 %s: %w", path, err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return fmt.Errorf("替换 %s 失败: %w", path, err)
	}
	return nil
}

// IsStateMissing 报告 LoadState 的错误是不是“文件还不存在”。
func IsStateMissing(err error) bool { return errors.Is(err, fs.ErrNotExist) }
