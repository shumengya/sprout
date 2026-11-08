package platform

import (
	"path/filepath"
	"strings"
	"testing"
)

// fakeProbe 用一个“存在的文件集合”和环境变量表构造 Probe。
func fakeProbe(files []string, env map[string]string, pathDirs ...string) Probe {
	set := map[string]bool{}
	for _, f := range files {
		set[f] = true
	}
	return Probe{
		Exists:   func(p string) bool { return set[p] },
		Getenv:   func(k string) string { return env[k] },
		PathDirs: func() []string { return pathDirs },
	}
}

func j(parts ...string) string { return filepath.Join(parts...) }

func TestIsWSLLauncher(t *testing.T) {
	cases := map[string]bool{
		`C:\Windows\System32\bash.exe`:                            true,
		`c:\windows\sysnative\bash.exe`:                           true,
		`C:\Users\a\AppData\Local\Microsoft\WindowsApps\bash.exe`: true,
		`C:\Program Files\Git\bin\bash.exe`:                       false,
		`C:\msys64\usr\bin\bash.exe`:                              false,
	}
	for path, want := range cases {
		if got := isWSLLauncher(path); got != want {
			t.Errorf("isWSLLauncher(%q) = %v，期望 %v", path, got, want)
		}
	}
}

func TestPwshEdition(t *testing.T) {
	if pwshEdition(j("a", "pwsh.exe")) != "7+" || pwshEdition(j("a", "PWSH.EXE")) != "7+" {
		t.Error("pwsh 应判定为 7+")
	}
	if pwshEdition(j("a", "powershell.exe")) != "5.1" {
		t.Error("powershell 应判定为 5.1")
	}
}

func TestWindowsPowerShellOrder(t *testing.T) {
	pf := j("C", "Program Files")
	sr := j("C", "Windows")
	env := map[string]string{"ProgramFiles": pf, "SystemRoot": sr}
	pwshPath := j("bin", "pwsh.exe")
	pwshPF := j(pf, "PowerShell", "7", "pwsh.exe")
	psPath := j("bin", "powershell.exe")
	psSys := j(sr, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")

	cases := []struct {
		name  string
		cfg   string
		files []string
		want  string
		ed    string
	}{
		{"配置优先", j("my", "pwsh.exe"), []string{j("my", "pwsh.exe"), pwshPath}, j("my", "pwsh.exe"), "7+"},
		{"PATH 的 pwsh", "", []string{pwshPath, pwshPF, psPath, psSys}, pwshPath, "7+"},
		{"ProgramFiles 的 pwsh", "", []string{pwshPF, psPath, psSys}, pwshPF, "7+"},
		{"PATH 的 powershell", "", []string{psPath, psSys}, psPath, "5.1"},
		{"System32 的 powershell", "", []string{psSys}, psSys, "5.1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sh, why := detectWindowsPowerShell(c.cfg, fakeProbe(c.files, env, "bin"))
			if sh == nil {
				t.Fatalf("应探测到 PowerShell，原因: %s", why)
			}
			if sh.Path != c.want || sh.Edition != c.ed {
				t.Fatalf("得到 %s (%s)，期望 %s (%s)", sh.Path, sh.Edition, c.want, c.ed)
			}
		})
	}
}

func TestWindowsPowerShellMissing(t *testing.T) {
	sh, why := detectWindowsPowerShell("", fakeProbe(nil, nil, "bin"))
	if sh != nil || why == "" {
		t.Fatalf("应不可用并给出原因，得到 %v / %q", sh, why)
	}
	sh, why = detectWindowsPowerShell(j("x", "pwsh.exe"), fakeProbe(nil, nil))
	if sh != nil || !strings.Contains(why, "powershellPath") {
		t.Fatalf("配置路径不存在应直接报错，得到 %v / %q", sh, why)
	}
}

func TestWindowsBashOrder(t *testing.T) {
	pf := j("C", "Program Files")
	pf86 := j("C", "Program Files (x86)")
	env := map[string]string{"ProgramFiles": pf, "ProgramFiles(x86)": pf86}
	gitPF := j(pf, "Git", "bin", "bash.exe")
	gitPF86 := j(pf86, "Git", "bin", "bash.exe")
	msys := j("msys", "bash.exe")

	sh, _ := detectWindowsBash("", fakeProbe([]string{gitPF, gitPF86, msys}, env, "msys"))
	if sh == nil || sh.Path != gitPF || sh.Label != "Git Bash" {
		t.Fatalf("应优先 Program Files 的 Git Bash，得到 %+v", sh)
	}
	sh, _ = detectWindowsBash("", fakeProbe([]string{gitPF86, msys}, env, "msys"))
	if sh == nil || sh.Path != gitPF86 {
		t.Fatalf("应使用 Program Files (x86) 的 Git Bash，得到 %+v", sh)
	}
	sh, _ = detectWindowsBash("", fakeProbe([]string{msys}, env, "msys"))
	if sh == nil || sh.Path != msys {
		t.Fatalf("应回退到 PATH 里的 bash.exe，得到 %+v", sh)
	}
	cfg := j("custom", "bash.exe")
	sh, _ = detectWindowsBash(cfg, fakeProbe([]string{cfg, gitPF}, env))
	if sh == nil || sh.Path != cfg {
		t.Fatalf("配置路径应优先，得到 %+v", sh)
	}
}

func TestWindowsBashRejectsWSLLauncher(t *testing.T) {
	wsl := `C:\Windows\System32\bash.exe`
	// PATH 里只有 WSL 启动器：不可用，提示里要说明原因。
	sh, why := detectWindowsBash("", fakeProbe([]string{filepath.Join(`C:\Windows\System32`, "bash.exe")}, nil, `C:\Windows\System32`))
	if sh != nil {
		t.Fatalf("WSL 启动器不应被当作 Git Bash: %+v", sh)
	}
	if !strings.Contains(why, "WSL") || !strings.Contains(why, "Git for Windows") {
		t.Fatalf("提示应说明 WSL 并建议安装 Git for Windows，得到: %s", why)
	}
	// 显式配置成 WSL 启动器同样拒绝。
	sh, why = detectWindowsBash(wsl, fakeProbe([]string{wsl}, nil))
	if sh != nil || !strings.Contains(why, "WSL") {
		t.Fatalf("配置为 WSL 启动器应被拒绝，得到 %v / %q", sh, why)
	}
	// 完全找不到：提示安装 Git。
	sh, why = detectWindowsBash("", fakeProbe(nil, nil, "bin"))
	if sh != nil || !strings.Contains(why, "未找到 Git Bash") {
		t.Fatalf("得到 %v / %q", sh, why)
	}
}

func TestWindowsBashSkipsWSLButFindsLaterBash(t *testing.T) {
	sys := `C:\Windows\System32`
	msys := j("msys", "bash.exe")
	files := []string{filepath.Join(sys, "bash.exe"), msys}
	sh, _ := detectWindowsBash("", fakeProbe(files, nil, sys, "msys"))
	if sh == nil || sh.Path != msys {
		t.Fatalf("应跳过 WSL 启动器并使用后面的 bash.exe，得到 %+v", sh)
	}
}

func TestDetectWindowsCombines(t *testing.T) {
	s := detectWindows("", "", fakeProbe(nil, nil))
	if s.Bash != nil || s.PowerShell != nil || len(s.Problems) != 2 {
		t.Fatalf("两个都不可用时应有两条 Problem，得到 %+v", s)
	}
}

func TestDetectUnix(t *testing.T) {
	bash := j("usr", "bin", "bash")
	s := detectUnix("", fakeProbe([]string{bash}, nil, j("usr", "bin")))
	if s.Bash == nil || s.Bash.Path != bash || s.PowerShell != nil || len(s.Problems) != 0 {
		t.Fatalf("得到 %+v", s)
	}
	s = detectUnix("", fakeProbe(nil, nil, j("usr", "bin")))
	if s.Bash != nil || len(s.Problems) != 1 {
		t.Fatalf("没有 bash 应报一条 Problem，得到 %+v", s)
	}
	s = detectUnix(j("nope", "bash"), fakeProbe([]string{bash}, nil, j("usr", "bin")))
	if s.Bash != nil || len(s.Problems) != 1 || !strings.Contains(s.Problems[0].Reason, "bashPath") {
		t.Fatalf("配置路径不存在应报错而不是回退，得到 %+v", s)
	}
}
