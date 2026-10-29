//go:build windows

package main

// CPU 占用率。
//
// 后台每 500 ms 用 GetSystemTimes 的差值算一次，结果放在原子变量里；
// 界面和「自动」模式都只读这个值，互不干扰（各自调用会把采样窗口切碎）。
//
// 不用驱动、不用管理员。

import (
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

var (
	modKernelCPU    = syscall.NewLazyDLL("kernel32.dll")
	procSystemTimes = modKernelCPU.NewProc("GetSystemTimes")

	cpuPct  int32
	cpuOnce sync.Once
)

type filetime struct {
	Low  uint32
	High uint32
}

func (f filetime) u64() uint64 { return uint64(f.High)<<32 | uint64(f.Low) }

// readCPUTimes 返回 (idle, total) 累计值。
func readCPUTimes() (uint64, uint64, bool) {
	var idle, kern, user filetime
	r, _, _ := procSystemTimes.Call(
		uintptr(unsafe.Pointer(&idle)),
		uintptr(unsafe.Pointer(&kern)),
		uintptr(unsafe.Pointer(&user)))
	if r == 0 {
		return 0, 0, false
	}
	return idle.u64(), kern.u64() + user.u64(), true
}

// startCPUMonitor 启动后台采样（只启动一次）。
func startCPUMonitor() {
	cpuOnce.Do(func() {
		go func() {
			lastIdle, lastAll, ok := readCPUTimes()
			if !ok {
				return
			}
			t := time.NewTicker(500 * time.Millisecond)
			defer t.Stop()
			for range t.C {
				idle, all, ok := readCPUTimes()
				if !ok {
					continue
				}
				di, da := idle-lastIdle, all-lastAll
				lastIdle, lastAll = idle, all
				if da == 0 {
					continue
				}
				pct := int((da - di) * 100 / da)
				if pct < 0 {
					pct = 0
				}
				if pct > 100 {
					pct = 100
				}
				atomic.StoreInt32(&cpuPct, int32(pct))
			}
		}()
	})
}

// cpuLoad 返回最近一次采样到的 CPU 占用率（0..100）。
func cpuLoad() int { return int(atomic.LoadInt32(&cpuPct)) }
