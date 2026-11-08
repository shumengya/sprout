//go:build windows

package platform

import (
	"syscall"
	"unsafe"
)

var (
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleCP          = kernel32.NewProc("GetConsoleCP")
	procGetConsoleOutputCP    = kernel32.NewProc("GetConsoleOutputCP")
	procSetConsoleCP          = kernel32.NewProc("SetConsoleCP")
	procSetConsoleOutputCP    = kernel32.NewProc("SetConsoleOutputCP")
	procGetConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")
)

const codePageUTF8 = 65001

// InitConsole 把控制台输入/输出代码页切到 UTF-8，并返回恢复函数。
// 子进程继承控制台代码页，原生程序（ipconfig 等）的中文输出因此也是 UTF-8；
// 退出时必须恢复，否则会污染从 cmd / PowerShell 里启动本程序的父控制台。
func InitConsole() (restore func()) {
	inCP, _, _ := procGetConsoleCP.Call()
	outCP, _, _ := procGetConsoleOutputCP.Call()
	if inCP == 0 && outCP == 0 {
		return func() {} // 没有附着的控制台（重定向或服务环境）
	}
	procSetConsoleCP.Call(codePageUTF8)
	procSetConsoleOutputCP.Call(codePageUTF8)
	return func() {
		if inCP != 0 {
			procSetConsoleCP.Call(inCP)
		}
		if outCP != 0 {
			procSetConsoleOutputCP.Call(outCP)
		}
	}
}

// OwnsConsole 报告本进程是否独占控制台，即双击 exe 启动、窗口会随进程退出而消失。
// 从 cmd / PowerShell / Windows Terminal 里启动时，shell 也附着在控制台上，返回 false。
func OwnsConsole() bool {
	var pids [2]uint32
	n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	return n == 1
}
