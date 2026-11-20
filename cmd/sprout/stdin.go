package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// maxStdinBytes 是 sprout run 从 stdin 读入的上限。再大的内容也塞不进上下文窗口，读完只会白占内存。
const maxStdinBytes = 8 << 20

// pipedStdin 在 stdin 是管道或重定向的文件时返回它，否则返回 nil。
// 终端和空设备（/dev/null、NUL）都是字符设备：前者读了会一直等用户输入，后者没有内容。
func pipedStdin(f *os.File) io.Reader {
	if f == nil {
		return nil
	}
	st, err := f.Stat()
	if err != nil || st.Mode()&os.ModeCharDevice != 0 {
		return nil
	}
	return f
}

// runPromptText 合成 sprout run 的提问。explicit 表示参数里写了单独的 "-"：stdin 的内容接在提问文字后面
// （git diff | sprout run 审一下这个改动 -）。没有任何文字和图片时也从 stdin 读（cat q.txt | sprout run）。
// 只在这两种情况下读：Git Bash（mintty）里 stdin 就算没有重定向也是管道，有提问时还去读会一直卡住。
// stdin 为 nil 表示它不是管道或文件。
func runPromptText(prompt string, explicit, hasImages bool, stdin io.Reader) (string, error) {
	implicit := prompt == "" && !hasImages
	if !explicit && !implicit {
		return prompt, nil
	}
	if stdin == nil {
		if explicit {
			return "", fmt.Errorf("%w: 写了 -，但 stdin 没有重定向，例如 git diff | sprout run 审一下这个改动 -", errUsage)
		}
		return "", fmt.Errorf("%w: run 需要提问内容，例如 sprout run \"看一下当前目录\"，或从 stdin 传入", errUsage)
	}
	b, err := io.ReadAll(io.LimitReader(stdin, maxStdinBytes+1))
	if err != nil {
		return "", fmt.Errorf("读取 stdin 失败: %w", err)
	}
	if len(b) > maxStdinBytes {
		return "", fmt.Errorf("stdin 的内容超过 %d MB", maxStdinBytes>>20)
	}
	in := strings.TrimRight(string(b), "\r\n")
	switch {
	case strings.TrimSpace(in) == "" && prompt == "" && !hasImages:
		return "", fmt.Errorf("%w: stdin 是空的，run 需要提问内容", errUsage)
	case strings.TrimSpace(in) == "":
		return prompt, nil
	case prompt == "":
		return in, nil
	}
	return prompt + "\n\n" + in, nil
}
