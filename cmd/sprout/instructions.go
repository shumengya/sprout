package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/config"
)

// maxInstructionBytes 是单份 AGENTS.md 进入系统提示词的上限。说明文件每次请求都要发送，
// 过大的文件（误放的日志、生成的文档）会长期挤占上下文。
const maxInstructionBytes = 32 * 1024

// loadInstructions 收集要拼进系统提示词的 AGENTS.md：先是数据目录里的全局说明，
// 再从工作目录的最上级到工作目录本身依次找项目说明（越靠后越具体）。
//
// 这些文件不归 sprout 管理（上级目录可能是别人的），所以读不了只记下问题、不阻止启动；
// 空文件跳过，不生成模板。
func loadInstructions(dataDir, cwd string) (docs []agent.Instruction, problems []string) {
	seen := map[string]bool{}
	add := func(path, scope string) {
		key := strings.ToLower(filepath.Clean(path)) // Windows 路径不区分大小写；其他平台同名不同大小写的祖先目录极少见
		if seen[key] {
			return
		}
		seen[key] = true
		text, err := readInstruction(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			problems = append(problems, fmt.Sprintf("读取 %s 失败，已跳过：%v", path, err))
		case strings.TrimSpace(text) != "":
			docs = append(docs, agent.Instruction{Path: path, Scope: scope, Text: text})
		}
	}

	add(config.AgentsPath(dataDir), "全局")
	// cwd 来自 os.Getwd，总是绝对路径；相对路径（只在测试里出现）没有可靠的上级，不往上找。
	if filepath.IsAbs(cwd) {
		var dirs []string
		for dir := filepath.Clean(cwd); ; {
			dirs = append(dirs, dir)
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
		slices.Reverse(dirs)
		for _, dir := range dirs {
			add(filepath.Join(dir, config.AgentsFileName), "项目")
		}
	}
	return docs, problems
}

// readInstruction 读取一份说明文件，超过上限时按字符边界截断并注明。
func readInstruction(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("是目录")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b := make([]byte, maxInstructionBytes+1)
	n, err := f.Read(b)
	if err != nil && n == 0 && info.Size() > 0 {
		return "", err
	}
	b = b[:n]
	if len(b) <= maxInstructionBytes {
		return string(b), nil
	}
	b = b[:maxInstructionBytes]
	for len(b) > 0 && !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return string(b) + fmt.Sprintf("\n\n[文件超过 %d KB，后面的内容已省略]", maxInstructionBytes/1024), nil
}
