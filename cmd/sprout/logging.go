package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// openLogger 打开 <logsDir>/sprout-YYYYMMDD.log，并删掉超过 logRetention 的旧日志。
// 日志是辅助手段，打不开就丢弃，不阻止程序启动。
// 日志里不记录 API Key、请求体、用户输入；命令只记截断后的前 200 字符，完整内容在会话 JSONL 里。
func openLogger(logsDir string, now time.Time) (*slog.Logger, io.Closer) {
	discard := slog.New(slog.DiscardHandler)
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return discard, io.NopCloser(nil)
	}
	cleanOldLogs(logsDir, now)
	name := filepath.Join(logsDir, fmt.Sprintf("sprout-%s.log", now.Format("20060102")))
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return discard, io.NopCloser(nil)
	}
	return slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo})), f
}

// logRetention 是日志保留时长。日志只用来排查问题，一天一个文件，更早的在启动时删掉。
const logRetention = 30 * 24 * time.Hour

// cleanOldLogs 删除文件名日期早于 now-logRetention 的 sprout-YYYYMMDD.log。
// 只认这个命名，目录里别的文件不碰；删不掉就算了，下次启动再试。
func cleanOldLogs(logsDir string, now time.Time) {
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		return
	}
	cutoff := now.Add(-logRetention)
	for _, e := range entries {
		day, ok := strings.CutPrefix(e.Name(), "sprout-")
		if !ok || e.IsDir() {
			continue
		}
		day, ok = strings.CutSuffix(day, ".log")
		if !ok {
			continue
		}
		t, err := time.ParseInLocation("20060102", day, now.Location())
		if err != nil || !t.Before(cutoff) {
			continue
		}
		os.Remove(filepath.Join(logsDir, e.Name()))
	}
}
