//go:build linux

package platform

import (
	"os"
	"runtime"
)

// procMemMB 读 /proc/self/status 的 VmRSS，解析在 parseVmRSSMB（sysusage.go）。
func procMemMB() (float64, bool) {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, false
	}
	return parseVmRSSMB(string(b))
}

// Android（Termux）的 GOOS 是 android，构建时同样满足 linux 标签。
// Android 8+ 的应用读不了 /proc/stat，sysCPUTimes 会返回 false，状态栏就不显示 CPU；/proc/meminfo 通常可读。

func sysMemPercent() (float64, bool) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	return parseMeminfo(string(b))
}

// sysLoadAvg 读 /proc/loadavg。Android 没有这个文件，返回 false。
func sysLoadAvg() (float64, bool) {
	if runtime.GOOS == "android" {
		return 0, false
	}
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, false
	}
	return parseLoadAvg(string(b))
}

func sysCPUTimes() (idle, total uint64, ok bool) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	return parseProcStat(string(b))
}
