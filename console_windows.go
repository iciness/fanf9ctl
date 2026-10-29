//go:build windows

package main

// 控制台窗口管理。
//
// 程序编译成 GUI 子系统（-H windowsgui），所以双击运行不会出现黑框。
// 需要看日志时（-debug，或 -list / -diag 这类命令行模式）再挂一个控制台：
//
//   - 如果 stdout 已经被父进程重定向（管道/文件），直接用，不动控制台
//   - 否则尝试附加到父控制台（在 cmd 里运行的情况）
//   - 再不行就自己 AllocConsole 开一个窗口

import (
	"log"
	"os"
	"syscall"
)

var (
	modKernelConsole  = syscall.NewLazyDLL("kernel32.dll")
	procAttachConsole = modKernelConsole.NewProc("AttachConsole")
	procAllocConsole  = modKernelConsole.NewProc("AllocConsole")
	procGetConsoleWnd = modKernelConsole.NewProc("GetConsoleWindow")
)

const attachParentProcess = ^uintptr(0) // (DWORD)-1

// ensureConsole 给命令行模式用：stdout 已经可用（管道/重定向）时就不动控制台，
// 否则附加到父控制台，实在没有就新开一个。
func ensureConsole() {
	if stdoutUsable() {
		return
	}
	if r, _, _ := procAttachConsole.Call(attachParentProcess); r == 0 {
		if r2, _, _ := procAllocConsole.Call(); r2 == 0 {
			return
		}
	}

	if f := openConsole("CONOUT$"); f != nil {
		os.Stdout = f
		os.Stderr = f
		log.SetOutput(f)
	}
	if f := openConsole("CONIN$"); f != nil {
		os.Stdin = f
	}
}

// showConsole 给 -debug 用：不管 stdout 是不是管道，都保证有一个控制台窗口。
func showConsole() {
	if hasConsole() {
		return
	}
	if r, _, _ := procAllocConsole.Call(); r == 0 {
		return
	}
	if f := openConsole("CONOUT$"); f != nil {
		os.Stdout = f
		os.Stderr = f
		log.SetOutput(f)
	}
	if f := openConsole("CONIN$"); f != nil {
		os.Stdin = f
	}
}

func stdoutUsable() bool {
	t, _ := syscall.GetFileType(syscall.Handle(os.Stdout.Fd()))
	return t != syscall.FILE_TYPE_UNKNOWN
}

// hasConsole 判断当前进程有没有挂控制台窗口。
func hasConsole() bool {
	h, _, _ := procGetConsoleWnd.Call()
	return h != 0
}

// openConsole 打开 CONOUT$ / CONIN$ 这类控制台设备。
func openConsole(name string) *os.File {
	p, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil
	}
	h, err := syscall.CreateFile(p,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE,
		nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil
	}
	return os.NewFile(uintptr(h), name)
}
