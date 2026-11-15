// Package gitstatus 读取工作目录的 git 分支和改动文件数，供状态栏显示。
package gitstatus

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Info 是一次读取的结果。OK 为 false 表示读不到（不是 git 仓库、没装 git、超时），状态栏不显示这几项。
type Info struct {
	Branch                   string
	Added, Modified, Deleted int
	// Ahead、Behind 是相对上游的提交数。没有上游（没配 remote 分支）时都是 0。
	Ahead, Behind int
	OK            bool
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
		case strings.HasPrefix(line, "# branch.ab "):
			// 形如 "# branch.ab +2 -1"：领先上游 2 个提交，落后 1 个。
			in.Ahead, in.Behind = parseBranchAB(strings.TrimPrefix(line, "# branch.ab "))
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

// parseBranchAB 解析 "# branch.ab" 后面的 "+a -b"。解析不了时返回 0，不猜。
func parseBranchAB(s string) (ahead, behind int) {
	for f := range strings.FieldsSeq(s) {
		v, ok := strings.CutPrefix(f, "+")
		if !ok {
			v, ok = strings.CutPrefix(f, "-")
			if !ok {
				continue
			}
			if n, err := strconv.Atoi(v); err == nil {
				behind = n
			}
			continue
		}
		if n, err := strconv.Atoi(v); err == nil {
			ahead = n
		}
	}
	return ahead, behind
}
