//go:build unix && !darwin && !android

package platform

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
)

// clipboardTypes 是按优先顺序认的图片类型。
var clipboardTypes = []string{"image/png", "image/jpeg", "image/webp", "image/gif"}

// clipboardImage 在 Wayland 下用 wl-paste，X11 下用 xclip：都先列出剪贴板里有哪些类型，再取图片那一种。
// xsel 读不了图片，所以不在候选里。
func clipboardImage() ([]byte, []string, error) {
	list, get := xclipCmds()
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		if _, err := exec.LookPath("wl-paste"); err == nil {
			list, get = wlCmds()
		}
	}
	if _, err := exec.LookPath(list[0]); err != nil {
		return nil, nil, errors.New("读取剪贴板图片需要 wl-clipboard（Wayland）或 xclip（X11）")
	}
	out, err := clipRun(list)
	if err != nil {
		return nil, nil, err
	}
	types := strings.Fields(string(out))
	has := func(t string) bool {
		for _, x := range types {
			if x == t {
				return true
			}
		}
		return false
	}
	for _, t := range clipboardTypes {
		if has(t) {
			b, err := clipRun(get(t))
			return b, nil, err
		}
	}
	if has("text/uri-list") {
		b, err := clipRun(get("text/uri-list"))
		return nil, parseURIList(string(b)), err
	}
	return nil, nil, nil
}

func wlCmds() ([]string, func(string) []string) {
	return []string{"wl-paste", "--list-types"},
		func(t string) []string { return []string{"wl-paste", "--no-newline", "--type", t} }
}

func xclipCmds() ([]string, func(string) []string) {
	return []string{"xclip", "-selection", "clipboard", "-t", "TARGETS", "-o"},
		func(t string) []string { return []string{"xclip", "-selection", "clipboard", "-t", t, "-o"} }
}

// clipRun 执行读取命令。剪贴板的持有方没响应时 xclip 会一直等，所以限时。
func clipRun(argv []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
	if err != nil {
		return nil, errors.New("读取剪贴板失败：" + argv[0] + " " + err.Error())
	}
	return out, nil
}
