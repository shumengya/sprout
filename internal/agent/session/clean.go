package session

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// CleanOlder 删除最后修改时间早于 cutoff 的会话（JSONL 连同同名的 .images / .agents 目录），
// 返回删掉的会话，从旧到新排列。修改时间就是最近一次使用的时间：恢复后继续对话会追加写入。
//
// active 是当前正在写的会话文件，不删。dryRun 时只列出、不删除。没有提问的空会话也算在内
// （列表里看不到它们，但同样占地方）。单个会话删不掉不中断，错误合并返回；
// 删空的分组目录顺手移除（目录里还有别的文件就留着）。
func CleanOlder(sessionsDir string, cutoff time.Time, active string, dryRun bool) ([]Brief, error) {
	files, err := filepath.Glob(filepath.Join(sessionsDir, "*", "*.jsonl"))
	if err != nil {
		return nil, err
	}
	activeAbs := ""
	if active != "" {
		activeAbs, _ = filepath.Abs(active)
	}
	var out []Brief
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil || !st.Mode().IsRegular() || !st.ModTime().Before(cutoff) {
			continue
		}
		if abs, _ := filepath.Abs(f); abs == activeAbs {
			continue
		}
		b, _ := briefOf(f) // 只为了显示标题；空会话也照样清理
		b.Path, b.Updated = f, st.ModTime()
		out = append(out, b)
	}
	slices.SortStableFunc(out, func(a, b Brief) int { return a.Updated.Compare(b.Updated) })
	if dryRun {
		return out, nil
	}
	var errs []error
	done := out[:0]
	groups := map[string]bool{}
	for _, b := range out {
		if err := Delete(sessionsDir, b.Path, active); err != nil {
			errs = append(errs, err)
			continue
		}
		done = append(done, b)
		groups[filepath.Dir(b.Path)] = true
	}
	for g := range groups {
		os.Remove(g) // 只删空目录；非空时失败是预期的
	}
	return done, errors.Join(errs...)
}
