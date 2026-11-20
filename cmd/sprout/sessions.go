package main

import (
	"fmt"
	"io"
	"time"

	"github.com/shumengya/sprout/internal/agent/config"
	"github.com/shumengya/sprout/internal/agent/session"
	"github.com/shumengya/sprout/internal/cli"
)

// runSessionsClean 实现 sprout sessions clean：删除（dry 时只列出）超过 age 没用过的会话。
// 列表写 stdout，便于脚本处理；错误写 stderr。
func runSessionsClean(dataDir string, age time.Duration, ageText string, dry bool, now time.Time, stdout, stderr io.Writer) int {
	removed, err := session.CleanOlder(config.SessionsDir(dataDir), now.Add(-age), "", dry)
	for _, b := range removed {
		title := b.Title()
		if title == "" {
			title = "（没有提问）"
		}
		fmt.Fprintf(stdout, "  %s  %s  %s\n", b.Updated.Format("2006-01-02 15:04"), title, b.Cwd)
	}
	switch {
	case len(removed) == 0 && err == nil:
		fmt.Fprintf(stdout, "没有超过 %s 没用过的会话。\n", ageText)
	case dry:
		fmt.Fprintf(stdout, "共 %d 个会话超过 %s 没用过（--dry-run，没有删除）。\n", len(removed), ageText)
	default:
		fmt.Fprintf(stdout, "已删除 %d 个会话。\n", len(removed))
	}
	if err != nil {
		fmt.Fprintln(stderr, "错误: 有会话没能删除:", err)
		return cli.ExitError
	}
	return cli.ExitOK
}
