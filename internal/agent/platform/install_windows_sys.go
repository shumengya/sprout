//go:build windows

package platform

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

// 这一层只做系统调用：读写注册表里的 PATH、广播环境变量变化、取短路径。
// 逻辑都在 install_windows.go，经 winHost 的函数字段注入，测试时换成替身，不碰真实注册表。

const (
	userEnvKey = `Environment`
	sysEnvKey  = `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`
)

// readUserPath 读 HKCU\Environment 的 Path。expand 表示原值是 REG_EXPAND_SZ（含 %VAR%），写回时保持类型。
// 值不存在时返回空串和 expand=true（新建时用 REG_EXPAND_SZ，和系统默认一致）。
func readUserPath() (string, bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, userEnvKey, registry.QUERY_VALUE)
	if err != nil {
		return "", false, fmt.Errorf("无法打开注册表 HKCU\\%s: %w", userEnvKey, err)
	}
	defer k.Close()
	v, typ, err := k.GetStringValue("Path")
	switch {
	case errors.Is(err, registry.ErrNotExist):
		return "", true, nil
	case err != nil:
		return "", false, fmt.Errorf("无法读取用户 PATH: %w", err)
	}
	return v, typ == registry.EXPAND_SZ, nil
}

func writeUserPath(v string, expand bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, userEnvKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("无法打开注册表 HKCU\\%s: %w", userEnvKey, err)
	}
	defer k.Close()
	if expand {
		err = k.SetExpandStringValue("Path", v)
	} else {
		err = k.SetStringValue("Path", v)
	}
	if err != nil {
		return fmt.Errorf("无法写入用户 PATH: %w", err)
	}
	return nil
}

// readSystemPath 只读：系统 PATH 要管理员权限才能改，这里只用来提示冲突。读不到就当没有。
func readSystemPath() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, sysEnvKey, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue("Path")
	if err != nil {
		return ""
	}
	return v
}

var procSendMessageTimeout = syscall.NewLazyDLL("user32.dll").NewProc("SendMessageTimeoutW")

// broadcastEnvChange 通知 Explorer 等程序重新读取环境变量（等同于在系统设置里改完点确定）。
// 只影响之后新开的终端；已经打开的终端不会变。失败不影响注册本身，所以不返回错误。
func broadcastEnvChange() {
	const (
		hwndBroadcast   = 0xffff
		wmSettingChange = 0x001a
		smtoAbortIfHung = 0x0002
	)
	env, err := syscall.UTF16PtrFromString("Environment")
	if err != nil {
		return
	}
	var result uintptr
	procSendMessageTimeout.Call(hwndBroadcast, wmSettingChange, 0, uintptr(unsafe.Pointer(env)),
		smtoAbortIfHung, 5000, uintptr(unsafe.Pointer(&result)))
}

// shortPath 返回 8.3 短路径；卷上关闭了短文件名时返回原路径。
func shortPath(p string) string {
	long, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return p
	}
	n, err := syscall.GetShortPathName(long, nil, 0)
	if err != nil || n == 0 {
		return p
	}
	buf := make([]uint16, n)
	if n, err = syscall.GetShortPathName(long, &buf[0], n); err != nil || n == 0 || int(n) > len(buf) {
		return p
	}
	return syscall.UTF16ToString(buf[:n])
}
