//go:build !windows

package shell

// printConsolePIDs 只在 Windows 上有意义（只有 Windows 存在子进程共享控制台的问题）；
// 这里留一个空实现，是为了让 shell_test.go 里共用的 helper 分支在其它平台上也能编译。
func printConsolePIDs() {}
