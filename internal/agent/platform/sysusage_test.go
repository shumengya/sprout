package platform

import (
	"math"
	"runtime"
	"testing"
	"time"
)

func near(a, b float64) bool { return math.Abs(a-b) < 0.01 }

func TestParseMeminfo(t *testing.T) {
	const sample = `MemTotal:       16384000 kB
MemFree:          500000 kB
MemAvailable:    4096000 kB
Buffers:          200000 kB
`
	got, ok := parseMeminfo(sample)
	if !ok || !near(got, 75) { // (16384000-4096000)/16384000
		t.Fatalf("parseMeminfo = %v, %v，期望 75", got, ok)
	}
}

func TestParseMeminfoRefusesToGuess(t *testing.T) {
	cases := map[string]string{
		"没有 MemAvailable": "MemTotal: 1000 kB\nMemFree: 100 kB\n", // 不拿 MemFree 凑数
		"没有 MemTotal":     "MemAvailable: 100 kB\n",
		"总量为 0":           "MemTotal: 0 kB\nMemAvailable: 0 kB\n",
		"可用大于总量":          "MemTotal: 100 kB\nMemAvailable: 200 kB\n",
		"空文件":             "",
		"值不是数字":           "MemTotal: abc kB\nMemAvailable: 1 kB\n",
	}
	for name, in := range cases {
		if got, ok := parseMeminfo(in); ok {
			t.Errorf("%s: 应拒绝，得到 %v", name, got)
		}
	}
}

func TestParseProcStat(t *testing.T) {
	// user nice system idle iowait irq softirq steal guest guest_nice
	const sample = "cpu  100 20 30 700 50 5 5 10 99 99\ncpu0 50 10 15 350 25 2 3 5 0 0\nintr 1 2 3\n"
	idle, total, ok := parseProcStat(sample)
	if !ok {
		t.Fatal("应能解析")
	}
	if idle != 750 { // idle + iowait
		t.Errorf("idle = %d，期望 750", idle)
	}
	if total != 920 { // 前八项；guest 已含在 user 里，不能再加
		t.Errorf("total = %d，期望 920（前八项之和，不含 guest）", total)
	}
}

func TestParseProcStatRejectsGarbage(t *testing.T) {
	for name, in := range map[string]string{
		"空":        "",
		"不是 cpu 行": "intr 1 2 3 4 5 6\n",
		"字段太少":     "cpu 1 2 3\n",
		"不是数字":     "cpu 1 2 3 x 5\n",
	} {
		if _, _, ok := parseProcStat(in); ok {
			t.Errorf("%s: 应拒绝", name)
		}
	}
	// 老内核只有前几项也能用。
	if idle, total, ok := parseProcStat("cpu 10 0 10 70 10\n"); !ok || idle != 80 || total != 100 {
		t.Errorf("只有五项: idle=%d total=%d ok=%v", idle, total, ok)
	}
}

func TestCPUPercent(t *testing.T) {
	if p, ok := cpuPercent(100, 1000, 180, 1100); !ok || !near(p, 20) { // 总共走了 100，其中空闲 80
		t.Errorf("cpuPercent = %v, %v，期望 20", p, ok)
	}
	for name, args := range map[string][4]uint64{
		"没有推进":      {100, 1000, 100, 1000},
		"总时间回退":     {100, 1000, 100, 900},
		"空闲时间回退":    {100, 1000, 90, 1100},
		"空闲比总时间走得多": {100, 1000, 300, 1100},
	} {
		if p, ok := cpuPercent(args[0], args[1], args[2], args[3]); ok {
			t.Errorf("%s: 应返回 false，得到 %v", name, p)
		}
	}
}

func TestSamplerNeedsTwoSamplesForCPU(t *testing.T) {
	idle, total := uint64(100), uint64(1000)
	s := &SystemSampler{
		mem: func() (float64, bool) { return 42, true },
		cpu: func() (uint64, uint64, bool) { return idle, total, true },
	}
	first := s.Sample()
	if first.MemPercent != 42 || first.CPUPercent >= 0 {
		t.Fatalf("第一次采样没有 CPU 可比，应为负数: %+v", first)
	}
	idle, total = 180, 1100
	if second := s.Sample(); !near(second.CPUPercent, 20) || second.MemPercent != 42 {
		t.Fatalf("第二次采样 = %+v，期望 CPU 20%%", second)
	}
	// 没有推进：不报，也不能把上一次的值当成这一次的。
	if third := s.Sample(); third.CPUPercent >= 0 {
		t.Errorf("时间没推进应为未知: %+v", third)
	}
}

func TestSamplerReportsUnavailableAsNegative(t *testing.T) {
	s := &SystemSampler{
		mem: func() (float64, bool) { return 0, false },
		cpu: func() (uint64, uint64, bool) { return 0, 0, false },
	}
	if u := s.Sample(); u.MemPercent >= 0 || u.CPUPercent >= 0 {
		t.Errorf("读不到的项应为负数，不能是 0（那会被当成“占用 0%%”）: %+v", u)
	}
}

func TestSamplerRebaselinesAfterAReadFailure(t *testing.T) {
	ok := true
	idle, total := uint64(100), uint64(1000)
	s := &SystemSampler{
		mem: func() (float64, bool) { return 1, true },
		cpu: func() (uint64, uint64, bool) { return idle, total, ok },
	}
	s.Sample()
	ok = false
	s.Sample() // 中间读失败
	// 数值要让陈旧的基准（100, 1000）算得出一个合法的百分比（约 97.5%），
	// 这样基准没丢的话测试才会失败；用会被“空闲回退”判断拒绝的数值，测试就测不出任何东西。
	ok, idle, total = true, 200, 5000
	if u := s.Sample(); u.CPUPercent >= 0 {
		t.Errorf("读失败之后应重新取基准，不能拿陈旧的基准算: %+v", u)
	}
	idle, total = 260, 5100 // 再采一次，有了新基准就能算出来：总共走了 100，空闲 60
	if u := s.Sample(); !near(u.CPUPercent, 40) {
		t.Errorf("重新取基准之后应能算出 CPU: %+v", u)
	}
}

// 在真实系统上跑一遍：不断言具体数字，只确认读得到、范围合理。macOS 等不支持的平台跳过。
func TestRealSystemSampler(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skipf("%s 不提供内存和 CPU 采样", runtime.GOOS)
	}
	s := NewSystemSampler()
	first := s.Sample()
	if first.MemPercent < 0 || first.MemPercent > 100 {
		t.Errorf("内存占用 %v 不在 0-100 之内", first.MemPercent)
	}
	time.Sleep(300 * time.Millisecond) // Windows 的系统时间精度约 15ms，留足余量
	second := s.Sample()
	if second.CPUPercent < 0 || second.CPUPercent > 100 {
		// Android 8+ 读不了 /proc/stat，这时 CPU 是未知，不算失败。
		if runtime.GOOS == "android" {
			t.Skip("Android 读不了 /proc/stat")
		}
		t.Errorf("CPU 占用 %v 不在 0-100 之内", second.CPUPercent)
	}
}
