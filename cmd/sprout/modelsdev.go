package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/shumengya/sprout/internal/agent/config"
	"github.com/shumengya/sprout/internal/cli"
	"github.com/shumengya/sprout/internal/tui"
)

// applyModelsDev 用 models.dev 目录补全配置里每个模型没写的 contextWindow、maxOutput，并带上价格。
// 直接改 cfg：之后的 Selected、All、Select 都拿到补全后的值，切换模型也不用再查一遍。
// 用了默认值的模型记一条日志，方便排查“为什么窗口是 400k”。
func applyModelsDev(cfg *config.File, dev config.ModelsDev, log *slog.Logger) {
	for name, p := range cfg.Providers {
		for id, m := range p.Models {
			spec, found := dev.Lookup(id, p.BaseURL)
			resolved, from := config.ResolveModel(m, spec, found)
			p.Models[id] = resolved
			if from != "" && log != nil {
				log.Info("模型参数未手写，已补全", "model", name+"/"+id, "source", from,
					"contextWindow", resolved.ContextWindow, "maxOutput", resolved.MaxOutput)
			}
		}
		cfg.Providers[name] = p
	}
}

// tuiPrice 把配置里的单价转成界面用的类型（tui 不 import config）。
func tuiPrice(p config.ModelPrice) tui.Price {
	return tui.Price{Input: p.Input, Output: p.Output, CacheRead: p.CacheRead, CacheWrite: p.CacheWrite, Known: p.Known()}
}

// updateModels 是 `sprout models update` 的入口：读配置、联网拉目录、写缓存、打印结果。
// 配置读不出来照样报错退出——没有配置就不知道要核对哪些模型。
func updateModels(dataDir string, stdout, stderr io.Writer) int {
	path := config.ModelsPath(dataDir)
	if _, err := config.EnsureTemplate(path); err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return cli.ExitError
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return cli.ExitError
	}
	msg, err := updateModelsDev(dataDir, cfg, nil)
	if err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return cli.ExitError
	}
	fmt.Fprintln(stdout, msg)
	return 0
}

// updateModelsDev 拉取 models.dev 目录写入缓存，并报告本次配置里哪些模型对得上。
func updateModelsDev(dataDir string, cfg *config.File, client *http.Client) (string, error) {
	dev, err := config.FetchModelsDev(dataDir, client)
	if err != nil {
		return "", err
	}
	var matched, missed []string
	for name, p := range cfg.Providers {
		for id := range p.Models {
			ref := name + "/" + id
			if _, ok := dev.Lookup(id, p.BaseURL); ok {
				matched = append(matched, ref)
			} else {
				missed = append(missed, ref)
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "已更新模型目录，缓存写入 %s\n", config.ModelsDevCachePath(dataDir))
	fmt.Fprintf(&b, "本次配置 %d 个模型：%d 个在 models.dev 找到", len(matched)+len(missed), len(matched))
	if len(missed) > 0 {
		fmt.Fprintf(&b, "，%d 个没找到（窗口与输出上限用 models.jsonc 的手写值，都没写则用默认值 上下文 %d、输出 %d）：%s",
			len(missed), config.DefaultContextWindow, config.DefaultMaxOutput, strings.Join(missed, "、"))
	}
	b.WriteString("。\n重启 sprout 后生效。")
	return b.String(), nil
}
