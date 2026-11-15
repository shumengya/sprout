//go:build !windows && !linux

package platform

// macOS 等其他平台：没有不依赖 CGO 的、能在这里验证的读取方式，状态栏不显示内存和 CPU，而不是显示猜出来的数字。

func sysMemPercent() (float64, bool) { return 0, false }

func sysCPUTimes() (idle, total uint64, ok bool) { return 0, 0, false }

func procMemMB() (float64, bool) { return 0, false }

// sysLoadAvg：macOS 的 load average 要靠 sysctl 或 CGO，这里不读，状态栏只显示 CPU 百分比。
func sysLoadAvg() (float64, bool) { return 0, false }
