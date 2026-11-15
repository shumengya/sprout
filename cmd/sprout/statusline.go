package main

import (
	"fmt"

	"github.com/shumengya/sprout/internal/agent/config"
	"github.com/shumengya/sprout/internal/tui"
)

// loadStatusline 读取 statusline.jsonc 并转成 TUI 的布局。
//
// 文件缺失或为空时写入模板（与 mcp.jsonc、tools.jsonc 一致）。项目名、行号、颜色写错时
// 不中止启动：状态栏是外观，退回默认布局并提示一句就够了，不该让整个程序起不来。
func loadStatusline(dataDir string) (items []tui.StatusItem, problems []string, err error) {
	path := config.StatuslinePath(dataDir)
	created, err := config.EnsureStatuslineTemplate(path)
	if err != nil {
		return nil, nil, err
	}
	f, err := config.LoadStatusline(path)
	if err != nil {
		// 语法错误是硬错误：用户改了文件但格式坏了，静默用默认布局会让人以为改动没生效。
		return nil, nil, fmt.Errorf("%w\n配置文件：%s", err, path)
	}
	if created {
		problems = append(problems, "已生成状态栏配置 "+path)
	}
	converted := make([]tui.StatusItem, 0, len(f.Items))
	for _, it := range f.Items {
		converted = append(converted, tui.StatusItem{
			Name: it.Name, Line: it.Line, Color: it.Color,
			Enabled: it.Enable == nil || *it.Enable, // 不写 enable 就是启用
		})
	}
	if probs := tui.ValidateStatusline(converted); len(probs) > 0 {
		problems = append(problems, append([]string{"状态栏配置有问题，已改用默认布局（" + path + "）："}, probs...)...)
		return nil, problems, nil
	}
	return tui.ResolveStatusline(converted), problems, nil
}
