package main

import (
	"context"
	"fmt"

	"github.com/shumengya/sprout/internal/agent/platform"
	"github.com/shumengya/sprout/internal/tui"
)

// autoUpdater 把命令行版的检查与安装接到 TUI 的启动检查和 /update 上。
// 只传当前版本和仓库，不写 stdout——结果是要显示在对话区里的一句话。
type autoUpdater struct {
	version string
	repo    string
}

var _ tui.Updater = autoUpdater{}

// Check 供启动时调用：只有确实有新版本才返回提示，其余情况（已最新、开发构建、查询失败）
// 一律返回空串。启动时网络不通或 GitHub 限流很常见，不该在界面上冒出一条错误。
func (u autoUpdater) Check() string {
	// 开发构建无从比较，也不该每次 go run 都去打 GitHub API。
	if !isReleaseVersion(u.version) {
		return ""
	}
	plan, err := checkUpdate(u.version, u.repo, nil)
	if err != nil || !plan.Newer {
		return ""
	}
	return fmt.Sprintf("有新版本可用：%s（当前 %s），输入 /update 更新。", plan.Latest, plan.Current)
}

// Update 供 /update 调用：查询→下载→校验→试运行→替换，任何一步失败都不动原程序。
func (u autoUpdater) Update() (string, error) {
	plan, err := checkUpdate(u.version, u.repo, nil)
	if err != nil {
		return "", err
	}
	switch {
	case plan.Dev:
		return fmt.Sprintf("当前是开发构建（%s），无法和发布版本比较；最新发布是 %s。", plan.Current, plan.Latest), nil
	case !plan.Newer:
		if compareVersions(plan.Current, plan.Latest) > 0 {
			return fmt.Sprintf("当前版本 %s 比最新发布 %s 还新，无需更新。", plan.Current, plan.Latest), nil
		}
		return fmt.Sprintf("已是最新版本（%s）。", plan.Current), nil
	}
	exe, err := platform.ResolveExecutable()
	if err != nil {
		return "", err
	}
	// 进度信息没法在对话区逐行刷，状态行已经显示“正在下载”，这里丢弃即可。
	if err := installUpdate(context.Background(), plan, exe, nil, nil); err != nil {
		return "", err
	}
	return fmt.Sprintf("已更新到 %s，重新启动 sprout 后生效。", plan.Latest), nil
}
