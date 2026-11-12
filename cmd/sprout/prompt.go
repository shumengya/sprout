package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/shumengya/sprout/internal/agent"
)

// loadSystemPrompt 读取数据目录的 SYSTEMPROMPT.md；缺失或为空时写入内置模板再读。
// 模板写错是致命错误：静默回退到内置模板会让用户以为自己的修改生效了。
func loadSystemPrompt(path string) (*agent.PromptTemplate, error) {
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist) || (err == nil && len(b) == 0):
		if err := os.WriteFile(path, []byte(agent.DefaultPromptTemplate), 0o600); err != nil {
			return nil, fmt.Errorf("写入系统提示词模板失败: %w", err)
		}
		b = []byte(agent.DefaultPromptTemplate)
	case err != nil:
		return nil, fmt.Errorf("读取系统提示词失败: %w", err)
	}
	p, err := agent.ParsePromptTemplate(string(b))
	if err != nil {
		return nil, fmt.Errorf("%s: %w\n修改后需要重启 sprout；删除或清空该文件会恢复内置模板。", path, err)
	}
	return p, nil
}
