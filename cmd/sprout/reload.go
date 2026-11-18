package main

import (
	"fmt"
	"os"
	"reflect"

	"github.com/shumengya/sprout/internal/agent/config"
	"github.com/shumengya/sprout/internal/tui"
)

// fileStamp 是文件的修改时间与大小，用来发现 models.jsonc 改过。比较它就够了，不必读内容。
type fileStamp struct{ mod, size int64 }

func stampOf(path string) (fileStamp, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{}, false
	}
	return fileStamp{fi.ModTime().UnixNano(), fi.Size()}, true
}

// modelsChanged 报告 models.jsonc 自上次读取后是否改过，由 TUI 定时在后台 goroutine 里调用。
//
// 要连续两次看到同一个新状态才算改完：编辑器保存时可能先清空再写入，第一眼看到的可能是半截文件，
// 读进来只会报一个用户没犯的语法错误。文件暂时不存在（先删后写）时同样等它回来。
func (a *app) modelsChanged() bool {
	st, ok := stampOf(config.ModelsPath(a.dataDir))
	a.mu.Lock()
	defer a.mu.Unlock()
	seen := a.modelsSeen
	a.modelsSeen = st
	return ok && st != a.modelsLoaded && st == seen
}

// reloadModels 重新读取 models.jsonc，返回刷新后的 Info 与给用户看的提示。
//
// 新配置有错时保持原样；读成功就换上新的模型列表（/model 立即可见），当前模型的设置（地址、Key、窗口、
// 思考档位等）或 shell 路径变了就重建 agent，对话历史原样带过去。当前模型被删掉时继续用原来的设置，
// 等用户自己 /model 换一个。它会替换 agent，只能在没有 Run 进行时调用。
func (a *app) reloadModels() (tui.Info, []string) {
	path := config.ModelsPath(a.dataDir)
	st, _ := stampOf(path) // 先取时间戳再读：读的过程中又被改了，下次检查还能发现
	a.mu.Lock()
	a.modelsLoaded = st // 不论成败都记下：同一份写错的内容不会每隔几秒报一次
	a.mu.Unlock()

	cfg, err := config.Load(path)
	if err != nil {
		return a.info, []string{"models.jsonc 改动有误，继续使用之前的配置：" + err.Error()}
	}
	applyModelsDev(cfg, config.LoadModelsDev(a.dataDir), a.log)
	old := a.cfg
	a.cfg = cfg
	a.log.Info("models.jsonc reloaded", "models", len(cfg.All()))

	notice := fmt.Sprintf("已重新读取 models.jsonc（共 %d 个模型）", len(cfg.All()))
	cur := a.sel.Ref()
	sel, err := cfg.Select(cur)
	switch {
	case err != nil:
		return a.info, []string{notice + fmt.Sprintf("；当前模型 %s 已不在配置里，继续按原来的设置使用，可以用 /model 换一个", cur)}
	case sameModel(sel, a.sel) && cfg.Shell == old.Shell:
		return a.info, []string{notice}
	}
	key, err := sel.APIKey(a.env.getenv)
	if err != nil {
		return a.info, []string{notice + "；当前模型的新设置没有生效，继续用原来的：" + err.Error()}
	}
	// 同一个模型换了协议时，旧协议的思维链同样不该回传。
	oldEffort, _, err := a.useModel(sel, key, sel.Provider.Protocol != a.sel.Provider.Protocol)
	if err != nil {
		return a.info, []string{notice + "；当前模型的新设置没有生效，继续用原来的：" + err.Error()}
	}
	notice += "；当前模型 " + cur + " 的新设置已生效"
	if a.info.Effort != "" && a.effort != oldEffort {
		notice += fmt.Sprintf("（思考程度 %s → %s，新配置里没有 %s 档位）", oldEffort, a.effort, oldEffort)
	}
	return a.info, []string{notice}
}

// sameModel 报告两次读到的同一个模型在运行时是否等价：provider 的连接设置与模型本身的参数都没变。
// 同一 provider 下增删别的模型不算。
func sameModel(x, y config.Selection) bool {
	px, py := x.Provider, y.Provider
	px.Models, py.Models = nil, nil
	return reflect.DeepEqual(px, py) && reflect.DeepEqual(x.Model, y.Model)
}
