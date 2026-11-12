//go:build windows

package shell

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

var procGetConsoleProcessList = syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleProcessList")

// consoleProcessCount 返回当前进程所连控制台上的进程数。
// 共享同一个控制台时能看到彼此（计数 >= 2）；各自有独立控制台时只有自己（计数为 1）。
func consoleProcessCount() int {
	var buf [64]uint32
	n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return int(n)
}

func printConsolePIDs() {
	fmt.Fprintf(os.Stdout, "console-processes=%d", consoleProcessCount())
}

// 工具子进程必须有自己的（隐藏）控制台，而不是共享 sprout 的控制台：
// 共享时孙进程直接写控制台的内容会画到 TUI 上（例如 Chrome 的 "DevTools listening on ..."），
// 也能抢走 TUI 的键盘输入。
func TestChildGetsOwnHiddenConsole(t *testing.T) {
	parent := consoleProcessCount()
	if parent <= 1 {
		// 父进程独占控制台时，就算没加 CREATE_NO_WINDOW 子进程也会数到 1，测不出区别。
		t.Skip("测试进程独占控制台，无法区分共享与独立")
	}
	out, err := Run(context.Background(), helperOpts(t, "consolepids", 10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(out.Stdout)
	n, convErr := strconv.Atoi(strings.TrimPrefix(line, "console-processes="))
	if convErr != nil {
		t.Fatalf("子进程输出 = %q", line)
	}
	if n != 1 {
		t.Fatalf("父进程控制台有 %d 个进程，子进程数到 %d，说明子进程共享了父进程的控制台", parent, n)
	}
}
