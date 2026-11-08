//go:build !windows

package platform

// InitConsole 在非 Windows 平台无需处理，返回空的恢复函数。
func InitConsole() (restore func()) { return func() {} }

// OwnsConsole 在非 Windows 平台恒为 false：终端窗口由用户的终端模拟器管理。
func OwnsConsole() bool { return false }
