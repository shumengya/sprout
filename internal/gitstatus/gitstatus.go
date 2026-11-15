// Package gitstatus 读取工作目录的 git 分支和改动文件数，供状态栏显示。
package gitstatus

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// Info 是一次读取的结果。OK 为 false 表示读不到（不是 git 仓库、没装 git、超时），状态栏不显示这两项。
type Info struct {
	Branch                   string
	Added, Modified, Deleted int
	OK                       bool
}

// Read 在 dir 下执行一次 git status。超时放得很短：状态栏采样不能被大仓库拖住。
func Read(dir string) Info {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "status", "--porcelain=v2", "--branch").Output()
	if err != nil {
		return Info{}
	}
	return Parse(string(out))
}

// Parse 解析 porcelain v2 的输出。每个路径只算一次：工作区状态不是 '.' 时取工作区，否则取暂存区。
// 未跟踪文件算新增；冲突（u 行）算修改；重命名、复制、类型变化算修改。
func Parse(text string) Info {
	var in Info
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "# branch.head "):
			in.Branch = strings.TrimPrefix(line, "# branch.head ")
			in.OK = true
		case strings.HasPrefix(line, "? "):
			in.Added++
		case strings.HasPrefix(line, "u "):
			in.Modified++
		case strings.HasPrefix(line, "1 ") || strings.HasPrefix(line, "2 "):
			if len(line) < 4 {
				continue
			}
			c := line[3]
			if c == '.' {
				c = line[2]
			}
			switch c {
			case 'A':
				in.Added++
			case 'D':
				in.Deleted++
			default:
				in.Modified++
			}
		}
	}
	return in
}
