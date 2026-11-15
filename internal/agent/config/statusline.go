package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// StatuslineTemplate 是首次运行时生成的 statusline.jsonc。
//
// 只有列在这里、且没有写 "enable": false 的条目才显示。去掉一行就隐藏一项，
// 调整数组顺序就是同一行内各项的先后。
const StatuslineTemplate = `// Sprout 状态栏配置（JSONC：支持 // 和 /* */ 注释，以及尾逗号）。
//
// 每条可以写：
//   name    项目名（见下面列出的全部）；写错会在启动时报错，避免拼错后悄悄少了一项
//   line    显示在第几行：1-3，默认取程序内置的行号
//   color   颜色：256 色编号（如 "75"）、#RRGGBB，或 red / cyan 这类颜色名；不写用内置颜色
//   enable  是否显示，默认 true。改成 false 与本条删掉效果相同
//
// 一行里各项的先后就是这里数组的顺序；某项这一行放不下时整项被截掉，不会换行。
// 修改后需要重启 sprout 才会生效。
{
  "items": [
    // 第一行：工作目录、本会话概况、整机资源
    { "name": "路径" },
    { "name": "对话轮次" },
    { "name": "耗时" },
    { "name": "内存" },
    { "name": "CPU" },

    // 第二行：token 用量、花费、上下文、模型
    { "name": "输入" },
    { "name": "输出" },
    { "name": "缓存" },
    { "name": "花费" },
    { "name": "上下文" },
    { "name": "模型" },
    { "name": "思考强度" },

    // 第三行：当前状态、扩展能力、Git
    { "name": "状态" },
    { "name": "速度" },
    { "name": "压缩" },
    { "name": "MCP" },
    { "name": "插件" },
    { "name": "技能" },
    { "name": "工具" },
    { "name": "Git" }
  ]
}
`

// StatuslinePath 是状态栏配置的路径。
func StatuslinePath(dataDir string) string { return joinDataDir(dataDir, "statusline.jsonc") }

// StatuslineItem 是配置里的一条。零值部分表示沿用内置值。
type StatuslineItem struct {
	Name   string `json:"name"`
	Line   int    `json:"line"`
	Color  string `json:"color"`
	Enable *bool  `json:"enable"`
}

// StatuslineFile 是 statusline.jsonc 的内容。
type StatuslineFile struct {
	Items []StatuslineItem `json:"items"`
}

// LoadStatusline 读取并解析 statusline.jsonc。语法与类型错误由 decodeJSONC 报出。
// 项目名、行号、颜色的校验在 tui 里做（那里才有全部项目与内置值与颜色）。
func LoadStatusline(path string) (*StatuslineFile, error) {
	var f StatuslineFile
	if err := decodeJSONC(path, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// EnsureStatuslineTemplate 在文件缺失或为空时写入模板，已有内容则原样保留。
func EnsureStatuslineTemplate(path string) (created bool, err error) {
	if info, statErr := os.Stat(path); statErr == nil && info.Size() > 0 {
		return false, nil
	} else if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
		return false, fmt.Errorf("检查状态栏配置失败: %w", statErr)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return false, fmt.Errorf("创建状态栏配置模板失败: %w", err)
	}
	if _, err := f.WriteString(StatuslineTemplate); err != nil {
		f.Close()
		return false, fmt.Errorf("写入状态栏配置模板失败: %w", err)
	}
	return true, f.Close()
}

// statuslineTemplateMatches 供测试比较模板与仓库里的示例文件，统一换行后再比。
func statuslineTemplateMatches(b []byte) bool {
	return bytes.Equal(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")), []byte(StatuslineTemplate))
}
