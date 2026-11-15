package platform

import "testing"

func TestParseVmRSSMB(t *testing.T) {
	got, ok := parseVmRSSMB("Name:\tsprout\nVmRSS:\t   20480 kB\nThreads:\t8\n")
	if !ok || !near(got, 20) {
		t.Fatalf("parseVmRSSMB = %v, %v，期望 20", got, ok)
	}
}

func TestParseVmRSSMBRefusesToGuess(t *testing.T) {
	for name, in := range map[string]string{
		"没有 VmRSS": "Name:\tsprout\n",
		"单位不是 kB":  "VmRSS: 1 MB\n",
		"值不是数字":    "VmRSS: abc kB\n",
		"空文件":      "",
	} {
		if got, ok := parseVmRSSMB(in); ok {
			t.Errorf("%s: 应拒绝，得到 %v", name, got)
		}
	}
}
