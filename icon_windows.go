//go:build windows

package main

// 程序图标：icon.ico 在编译期嵌进二进制，运行时用来设窗口图标和托盘图标。
//
// exe 文件本身的图标由 rsrc 生成的 rsrc_windows_amd64.syso 提供（PE 资源段），
// 和这里用的是同一份 icon.ico。

import (
	_ "embed"
	"encoding/binary"
	"syscall"
	"unsafe"
)

//go:embed icon.ico
var iconICO []byte

var procCreateIconFromResourceEx = syscall.NewLazyDLL("user32.dll").
	NewProc("CreateIconFromResourceEx")

// icoEntry 是 ICO 目录里的一项。
type icoEntry struct {
	size   int
	offset uint32
	length uint32
}

// icoEntries 解析 ICO 目录。
func icoEntries() []icoEntry {
	if len(iconICO) < 6 {
		return nil
	}
	if binary.LittleEndian.Uint16(iconICO[0:2]) != 0 ||
		binary.LittleEndian.Uint16(iconICO[2:4]) != 1 {
		return nil
	}
	n := int(binary.LittleEndian.Uint16(iconICO[4:6]))
	out := make([]icoEntry, 0, n)
	for i := 0; i < n; i++ {
		off := 6 + i*16
		if off+16 > len(iconICO) {
			break
		}
		w := int(iconICO[off])
		if w == 0 {
			w = 256 // 0 表示 256
		}
		out = append(out, icoEntry{
			size:   w,
			length: binary.LittleEndian.Uint32(iconICO[off+8:]),
			offset: binary.LittleEndian.Uint32(iconICO[off+12:]),
		})
	}
	return out
}

// loadIcon 取最接近 size 的一张图，做成 HICON。失败返回 0。
func loadIcon(size int) uintptr {
	entries := icoEntries()
	if len(entries) == 0 {
		return 0
	}
	best := entries[0]
	found := false
	for _, e := range entries {
		if e.size >= size && (!found || e.size < best.size) {
			best, found = e, true
		}
	}
	if !found { // 都比要的大？退而求其次用最大的
		for _, e := range entries {
			if e.size > best.size {
				best = e
			}
		}
	}
	if int(best.offset)+int(best.length) > len(iconICO) || best.length == 0 {
		return 0
	}
	h, _, _ := procCreateIconFromResourceEx.Call(
		uintptr(unsafe.Pointer(&iconICO[best.offset])),
		uintptr(best.length),
		1,          // fIcon
		0x00030000, // dwVer
		uintptr(size),
		uintptr(size),
		0, // LR_DEFAULTCOLOR
	)
	return h
}
