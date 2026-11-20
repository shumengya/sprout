package main

import (
	"errors"
	"strings"
	"testing"
)

func TestParseArgs(t *testing.T) {
	prompt := func(s string) *string { return &s }
	cases := []struct {
		name    string
		argv    []string
		dataDir string
		prompt  *string
		version bool
		help    bool
	}{
		{"无参数启动 TUI", nil, "", nil, false, false},
		{"数据目录", []string{"--data-dir", "D:/x"}, "D:/x", nil, false, false},
		{"等号写法", []string{"--data-dir=D:/x"}, "D:/x", nil, false, false},
		{"run", []string{"run", "看一下目录"}, "", prompt("看一下目录"), false, false},
		{"run 多个词合并", []string{"run", "看", "一下", "目录"}, "", prompt("看 一下 目录"), false, false},
		{"数据目录在 run 之前", []string{"--data-dir", "D:/x", "run", "q"}, "D:/x", prompt("q"), false, false},
		{"数据目录在 run 之后", []string{"run", "--data-dir", "D:/x", "q"}, "D:/x", prompt("q"), false, false},
		{"version", []string{"--version"}, "", nil, true, false},
		{"help", []string{"-h"}, "", nil, false, true},
		{"help 长写法", []string{"--help"}, "", nil, false, true},
		{"run 带一张图", []string{"run", "--image", "a.png", "看图"}, "", prompt("看图"), false, false},
		{"run 只有图没有文字", []string{"run", "--image", "a.png"}, "", prompt(""), false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, err := parseArgs(c.argv)
			if err != nil {
				t.Fatal(err)
			}
			if a.dataDir != c.dataDir || a.version != c.version || a.help != c.help {
				t.Fatalf("得到 %+v", a)
			}
			if (a.runPrompt == nil) != (c.prompt == nil) || (c.prompt != nil && *a.runPrompt != *c.prompt) {
				t.Fatalf("runPrompt = %v，期望 %v", a.runPrompt, c.prompt)
			}
		})
	}
}

// 没有提问的 run 在解析阶段不报错：要看 stdin 有没有重定向，由 runPromptText 在运行时判断。
func TestParseRunStdinAndInteractive(t *testing.T) {
	a, err := parseArgs([]string{"run", "审一下", "-"})
	if err != nil || !a.runStdin || *a.runPrompt != "审一下" || a.interactive {
		t.Fatalf("单独的 - 应标记读 stdin: %+v %v", a, err)
	}
	a, err = parseArgs([]string{"run"})
	if err != nil || a.runPrompt == nil || *a.runPrompt != "" || a.runStdin {
		t.Fatalf("空 run 应留给运行时判断: %+v %v", a, err)
	}
	for _, argv := range [][]string{{"run", "-i"}, {"run", "--interactive", "-c", "先问这个"}} {
		a, err = parseArgs(argv)
		if err != nil || !a.interactive || a.runPrompt == nil {
			t.Fatalf("%v 应进入交互模式: %+v %v", argv, a, err)
		}
	}
	if *a.runPrompt != "先问这个" || a.resume != "latest" {
		t.Errorf("交互模式应保留首个提问和 -c: %+v", a)
	}
}

func TestParseModelsUpdate(t *testing.T) {
	a, err := parseArgs([]string{"models", "update", "--data-dir", "D:/x"})
	if err != nil {
		t.Fatal(err)
	}
	if !a.modelsUpdate || a.dataDir != "D:/x" {
		t.Fatalf("得到 %+v", a)
	}
	// --data-dir 写在前面也应生效。
	a, err = parseArgs([]string{"--data-dir", "D:/y", "models", "update"})
	if err != nil || !a.modelsUpdate || a.dataDir != "D:/y" {
		t.Fatalf("前置选项应保留: %+v %v", a, err)
	}
	for _, argv := range [][]string{{"models"}, {"models", "list"}, {"models", "update", "多余"}} {
		if _, err := parseArgs(argv); !errors.Is(err, errUsage) {
			t.Errorf("%v 应是用法错误，得到 %v", argv, err)
		}
	}
}

func TestParseServeArgs(t *testing.T) {
	a, err := parseArgs([]string{"--data-dir", "D:/x", "serve", "-c", "--effort", "high"})
	if err != nil || !a.serve || a.dataDir != "D:/x" || a.resume != "latest" || a.effort != "high" || a.runPrompt != nil {
		t.Fatalf("serve 选项解析不对: %+v %v", a, err)
	}
}

func TestParseArgsErrors(t *testing.T) {
	cases := map[string][]string{
		"未知命令":        {"bogus"},
		"serve 多余":    {"serve", "extra"},
		"未知选项":        {"--nope"},
		"run -i 又写 -": {"run", "-i", "-"},
		"选项缺值":        {"--data-dir"},
		"run 选项错":     {"run", "--bogus", "q"},
	}
	for name, argv := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parseArgs(argv)
			if !errors.Is(err, errUsage) {
				t.Fatalf("应返回 errUsage，得到 %v", err)
			}
		})
	}
}

func TestUsageMentionsDataDirPrecedence(t *testing.T) {
	for _, want := range []string{"--data-dir", "SPROUT_DATA_DIR", "go run", "sprout run"} {
		if !strings.Contains(usage, want) {
			t.Errorf("用法说明缺少 %q", want)
		}
	}
}
