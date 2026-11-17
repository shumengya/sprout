//go:build windows

package platform

import (
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetSystemTimes       = kernel32.NewProc("GetSystemTimes")
	// K32 前缀的版本在 kernel32 里，不必再加载 psapi.dll。
	procGetProcessMemoryInfo = kernel32.NewProc("K32GetProcessMemoryInfo")
)

// processMemoryCounters 对应 Win32 的 PROCESS_MEMORY_COUNTERS。
type processMemoryCounters struct {
	Cb                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

// pdh.dll 不在 KnownDLLs 里，只从 System32 加载，避免被工作目录里的同名 DLL 劫持。
var (
	pdh                             = windows.NewLazySystemDLL("pdh.dll")
	procPdhOpenQueryW               = pdh.NewProc("PdhOpenQueryW")
	procPdhAddEnglishCounterW       = pdh.NewProc("PdhAddEnglishCounterW")
	procPdhCollectQueryData         = pdh.NewProc("PdhCollectQueryData")
	procPdhGetFormattedCounterValue = pdh.NewProc("PdhGetFormattedCounterValue")
)

const pdhFmtDouble = 0x00000200

// pdhFmtCounterValue 对应 PDH_FMT_COUNTERVALUE（取 double 的那一支）；CStatus 后面有 4 字节对齐填充。
type pdhFmtCounterValue struct {
	CStatus     uint32
	_           uint32
	DoubleValue float64
}

// winLoad 是 Windows 上模拟的 1 分钟负载：Windows 没有 load average，
// 这里对“处理器队列长度”（就绪但等不到 CPU 的线程数）做 1 分钟时间常数的指数平均。
// 它不含正在运行的线程，数值通常比同等忙碌的 Linux 小，只能看趋势。
// 从 sprout 启动时的 0 开始累积，头一分钟偏低。
var winLoad struct {
	mu       sync.Mutex
	inited   bool
	failed   bool
	query    uintptr
	counter  uintptr
	avg      float64
	lastTime time.Time
}

// sysLoadAvg 采一次处理器队列长度并更新平均值。PDH 打不开（被精简的系统、计数器损坏）时以后都不再尝试。
func sysLoadAvg() (float64, bool) {
	l := &winLoad
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failed {
		return 0, false
	}
	if !l.inited {
		l.inited = true
		if procPdhOpenQueryW.Find() != nil || procPdhAddEnglishCounterW.Find() != nil ||
			procPdhCollectQueryData.Find() != nil || procPdhGetFormattedCounterValue.Find() != nil {
			l.failed = true
			return 0, false
		}
		if r, _, _ := procPdhOpenQueryW.Call(0, 0, uintptr(unsafe.Pointer(&l.query))); r != 0 {
			l.failed = true
			return 0, false
		}
		// 用英文计数器名：中文系统里本地化的计数器名不是 Processor Queue Length。
		path, _ := syscall.UTF16PtrFromString(`\System\Processor Queue Length`)
		if r, _, _ := procPdhAddEnglishCounterW.Call(l.query, uintptr(unsafe.Pointer(path)), 0, uintptr(unsafe.Pointer(&l.counter))); r != 0 {
			l.failed = true
			return 0, false
		}
	}
	if r, _, _ := procPdhCollectQueryData.Call(l.query); r != 0 {
		return 0, false
	}
	var v pdhFmtCounterValue
	if r, _, _ := procPdhGetFormattedCounterValue.Call(l.counter, pdhFmtDouble, 0, uintptr(unsafe.Pointer(&v))); r != 0 || v.CStatus != 0 {
		return 0, false
	}
	now := time.Now()
	if !l.lastTime.IsZero() {
		l.avg = decayLoad(l.avg, v.DoubleValue, now.Sub(l.lastTime))
	}
	l.lastTime = now
	return l.avg, true
}

// procMemMB 返回本进程的工作集（任务管理器里的“内存”），单位 MB。
func procMemMB() (float64, bool) {
	h, err := syscall.GetCurrentProcess()
	if err != nil {
		return 0, false
	}
	var c processMemoryCounters
	c.Cb = uint32(unsafe.Sizeof(c))
	r, _, _ := procGetProcessMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&c)), uintptr(c.Cb))
	if r == 0 {
		return 0, false
	}
	return float64(c.WorkingSetSize) / (1 << 20), true
}

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
