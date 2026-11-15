package platform

import (
	"bufio"
	"strconv"
	"strings"
	"sync"
)

// SystemUsage 是整机的内存和 CPU 占用百分比（0-100）。读不到的项为负数：
// 平台不支持、权限不够（Android 8+ 读不了 /proc/stat），或者 CPU 还没有第二次采样可比。
type SystemUsage struct {
	MemPercent float64
	CPUPercent float64
	// ProcMemMB 是本进程（sprout）占用的物理内存，单位 MB；读不到时为负数。
	ProcMemMB float64
}

// SystemSampler 采样整机资源占用。CPU 占用是两次采样之间的差，所以第一次采样没有 CPU 值。
// 可以被多个 goroutine 调用。
type SystemSampler struct {
	mu   sync.Mutex
	mem  func() (float64, bool)
	cpu  func() (idle, total uint64, ok bool)
	proc func() (float64, bool)

	prevIdle, prevTotal uint64
	havePrev            bool
}

// NewSystemSampler 返回读取真实系统的采样器。平台差异在 sysMemPercent / sysCPUTimes / procMemMB 里。
func NewSystemSampler() *SystemSampler {
	return &SystemSampler{mem: sysMemPercent, cpu: sysCPUTimes, proc: procMemMB}
}

// Sample 采样一次。
func (s *SystemSampler) Sample() SystemUsage {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := SystemUsage{MemPercent: -1, CPUPercent: -1, ProcMemMB: -1}
	if p, ok := s.mem(); ok {
		u.MemPercent = p
	}
	if mb, ok := s.proc(); ok {
		u.ProcMemMB = mb
	}
	idle, total, ok := s.cpu()
	if !ok {
		s.havePrev = false
		return u
	}
	if s.havePrev {
		if p, ok := cpuPercent(s.prevIdle, s.prevTotal, idle, total); ok {
			u.CPUPercent = p
		}
	}
	s.prevIdle, s.prevTotal, s.havePrev = idle, total, true
	return u
}

// cpuPercent 由两次累计时间算出这段时间内的忙碌比例。累计值回退（计数器重置）或没有推进时返回 false。
func cpuPercent(prevIdle, prevTotal, idle, total uint64) (float64, bool) {
	if total <= prevTotal || idle < prevIdle {
		return 0, false
	}
	dTotal, dIdle := total-prevTotal, idle-prevIdle
	if dIdle > dTotal {
		return 0, false
	}
	return float64(dTotal-dIdle) * 100 / float64(dTotal), true
}

// parseMeminfo 从 /proc/meminfo 的内容算出内存占用：(MemTotal - MemAvailable) / MemTotal。
// 没有 MemAvailable（很老的内核）时返回 false，不拿 MemFree 凑数——它不含可回收的缓存，会把占用高估很多。
func parseMeminfo(text string) (float64, bool) {
	var total, avail uint64
	var haveTotal, haveAvail bool
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		key, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest) // 形如 "16384256 kB"
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			total, haveTotal = v, true
		case "MemAvailable":
			avail, haveAvail = v, true
		}
	}
	if !haveTotal || !haveAvail || total == 0 || avail > total {
		return 0, false
	}
	return float64(total-avail) * 100 / float64(total), true
}

// parseVmRSSMB 从 /proc/self/status 的内容取出本进程驻留内存（VmRSS），换算成 MB。
// 没有 VmRSS 或单位不是 kB 时返回 false，不猜。
func parseVmRSSMB(text string) (float64, bool) {
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		key, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok || key != "VmRSS" {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) != 2 || fields[1] != "kB" {
			return 0, false
		}
		v, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return 0, false
		}
		return float64(v) / 1024, true
	}
	return 0, false
}

// parseProcStat 从 /proc/stat 的内容取出整机 CPU 的累计时间。
// 第一行 "cpu  user nice system idle iowait irq softirq steal ..."：idle 把 iowait 也算进去，
// total 取前八项（guest 已经含在 user 里，不能再加）。
func parseProcStat(text string) (idle, total uint64, ok bool) {
	line, _, _ := strings.Cut(text, "\n")
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, false
	}
	var vals [8]uint64
	n := min(len(fields)-1, len(vals))
	for i := 0; i < n; i++ {
		v, err := strconv.ParseUint(fields[i+1], 10, 64)
		if err != nil {
			return 0, 0, false
		}
		vals[i] = v
		total += v
	}
	idle = vals[3] + vals[4]
	return idle, total, true
}
