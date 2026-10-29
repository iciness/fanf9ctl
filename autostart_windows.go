//go:build windows

package main

// 开机启动：写 HKCU\Software\Microsoft\Windows\CurrentVersion\Run。
//
// 用 HKCU 而不是 HKLM，不需要管理员权限。值形如：
//
//	"C:\path\fanf9ctl.exe" -tray
//
// 带 -tray 表示启动后只驻留托盘，不弹窗口。

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	procRegCreateKeyExW = modAdvapi32.NewProc("RegCreateKeyExW")
	procRegSetValueExW  = modAdvapi32.NewProc("RegSetValueExW")
	procRegDeleteValueW = modAdvapi32.NewProc("RegDeleteValueW")
)

const (
	hkeyCurrentUser = 0x80000001
	keySetValue     = 0x0002
	keyQueryValue   = 0x0001
	regSZ           = 1
	runKeyPath      = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValueName    = "fanf9ctl"
)

// autostartCommand 返回要写进注册表的命令行。
func autostartCommand() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return `"` + exe + `" -tray`
}

// openRunKey 打开（不存在则以 KEY_SET_VALUE 创建）Run 键。
func openRunKey() (syscall.Handle, error) {
	sub, err := syscall.UTF16PtrFromString(runKeyPath)
	if err != nil {
		return 0, err
	}
	var h syscall.Handle
	var disp uint32
	r, _, _ := procRegCreateKeyExW.Call(
		hkeyCurrentUser, uintptr(unsafe.Pointer(sub)), 0, 0, 0,
		keySetValue|keyQueryValue, 0,
		uintptr(unsafe.Pointer(&h)), uintptr(unsafe.Pointer(&disp)))
	if int32(r) != 0 {
		return 0, fmt.Errorf("RegCreateKeyEx 失败: %d", r)
	}
	return h, nil
}

// autostartEnabled 判断注册表里有没有我们的启动项。
func autostartEnabled() bool {
	h, err := openRunKey()
	if err != nil {
		return false
	}
	defer procRegCloseKey.Call(uintptr(h))

	name, _ := syscall.UTF16PtrFromString(runValueName)
	var size uint32
	r, _, _ := procRegQueryValueEx.Call(uintptr(h), uintptr(unsafe.Pointer(name)), 0, 0, 0,
		uintptr(unsafe.Pointer(&size)))
	return int32(r) == 0
}

// setAutostart 打开/关闭开机启动。
func setAutostart(on bool) error {
	h, err := openRunKey()
	if err != nil {
		return err
	}
	defer procRegCloseKey.Call(uintptr(h))

	name, err := syscall.UTF16PtrFromString(runValueName)
	if err != nil {
		return err
	}

	if !on {
		r, _, _ := procRegDeleteValueW.Call(uintptr(h), uintptr(unsafe.Pointer(name)))
		if int32(r) != 0 && r != 2 { // 2 = ERROR_FILE_NOT_FOUND，没装过也算成功
			return fmt.Errorf("删除启动项失败: %d", r)
		}
		return nil
	}

	cmd := autostartCommand()
	if cmd == "" {
		return fmt.Errorf("取程序路径失败")
	}
	val, err := syscall.UTF16FromString(cmd)
	if err != nil {
		return err
	}
	r, _, _ := procRegSetValueExW.Call(uintptr(h), uintptr(unsafe.Pointer(name)), 0, regSZ,
		uintptr(unsafe.Pointer(&val[0])), uintptr(len(val)*2))
	if int32(r) != 0 {
		return fmt.Errorf("写入启动项失败: %d", r)
	}
	return nil
}
