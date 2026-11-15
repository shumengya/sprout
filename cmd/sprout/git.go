package main

import (
	"github.com/shumengya/sprout/internal/gitstatus"
	"github.com/shumengya/sprout/internal/tui"
)

// gitSampler 把 gitstatus 适配成 tui.Options.Git：状态栏据此显示分支和改动文件数。
func gitSampler(dir string) func() tui.GitInfo {
	return func() tui.GitInfo {
		g := gitstatus.Read(dir)
		return tui.GitInfo{Branch: g.Branch, Added: g.Added, Modified: g.Modified, Deleted: g.Deleted, OK: g.OK}
	}
}
