//go:build !windows

package platform

// DetectShells 探测当前系统上可用的 bash。PowerShell 工具只在 Windows 提供，
// cfgPS 在这里被忽略。
func DetectShells(cfgBash, _ string) Shells {
	return detectUnix(cfgBash, OSProbe())
}
