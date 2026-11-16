package platform

import (
	"os/exec"
	"runtime"
)

// OpenURL 用系统默认浏览器打开 url，不等浏览器退出。失败时调用方应把地址打印出来让用户手动打开：
// 没有图形界面的服务器、SSH 会话里本来就打不开。
func OpenURL(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// 不走 cmd /c start：URL 里的 & 会被 cmd 当成命令分隔符。
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	case "android":
		cmd = exec.Command("termux-open-url", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() //nolint:errcheck // 只为回收子进程
	return nil
}
