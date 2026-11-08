package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// openLogger 打开 <logsDir>/sprout-YYYYMMDD.log。日志是辅助手段，打不开就丢弃，不阻止程序启动。
// 日志里不记录 API Key、请求体、用户输入；命令只记截断后的前 200 字符，完整内容在会话 JSONL 里。
func openLogger(logsDir string, now time.Time) (*slog.Logger, io.Closer) {
	discard := slog.New(slog.DiscardHandler)
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return discard, io.NopCloser(nil)
	}
	name := filepath.Join(logsDir, fmt.Sprintf("sprout-%s.log", now.Format("20060102")))
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return discard, io.NopCloser(nil)
	}
	return slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo})), f
}
