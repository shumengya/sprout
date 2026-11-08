//go:build windows

package platform

// DetectShells 探测当前系统上可用的 bash / powershell。
// cfgBash / cfgPS 是 models.jsonc 里 shell 段的显式配置，留空则自动探测。
func DetectShells(cfgBash, cfgPS string) Shells {
	return detectWindows(cfgBash, cfgPS, OSProbe())
}
