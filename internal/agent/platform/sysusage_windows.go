//go:build windows

package platform

import (
	"syscall"
	"unsafe"
)

var (
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetSystemTimes       = kernel32.NewProc("GetSystemTimes")
)

// memoryStatusEx 对应 Win32 的 MEMORYSTATUSEX。
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32 // 物理内存占用百分比，系统已经算好
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func sysMemPercent() (float64, bool) {
	st := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&st)))
	if r == 0 {
		return 0, false
	}
	return float64(st.MemoryLoad), true
}

func filetimeUint64(ft syscall.Filetime) uint64 {
	return uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
}

// sysCPUTimes 返回整机累计的空闲时间和总时间。GetSystemTimes 的内核时间已经包含空闲时间，所以总时间是内核 + 用户。
func sysCPUTimes() (idle, total uint64, ok bool) {
	var idleT, kernelT, userT syscall.Filetime
	r, _, _ := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&idleT)), uintptr(unsafe.Pointer(&kernelT)), uintptr(unsafe.Pointer(&userT)))
	if r == 0 {
		return 0, 0, false
	}
	return filetimeUint64(idleT), filetimeUint64(kernelT) + filetimeUint64(userT), true
}
