//go:build windows

package main

// 诊断模式：把 GATT 的每一步单独打出来（含原始 HRESULT），
// 便于在真机上定位"卡在哪一层"。
//
//	fanf9ctl.exe -diag              # 用 config.json 里的 deviceName
//	fanf9ctl.exe -diag -device F9_BT
//
// 可用环境变量做实验（正常使用不需要）：
//
//	FANF9_ACCESS   CreateFile 的 dwDesiredAccess（默认 0xC0000000）
//	FANF9_SHARE    CreateFile 的 dwShareMode（默认 3）
//	FANF9_FLAGS    CreateFile 的 dwFlagsAndAttributes（默认 0）
//	FANF9_PATH     强制指定接口路径
//	FANF9_VERBOSE  =1 时打印完整服务/特征树

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

func envU32(name string, def uint32) uint32 {
	s := os.Getenv(name)
	if s == "" {
		return def
	}
	v, err := strconv.ParseUint(s, 0, 32)
	if err != nil {
		fmt.Printf("（%s=%q 解析失败，用默认值）\n", name, s)
		return def
	}
	return uint32(v)
}

// RunDiag 打印指定设备的 GATT 树，并逐步尝试读/写/订阅/注册通知。
func RunDiag(match string) error {
	devs, err := ListDevices()
	if err != nil {
		return err
	}
	want := strings.ToLower(strings.TrimSpace(match))
	var picked *DeviceInfo
	for i := range devs {
		d := &devs[i]
		if want == "" || strings.Contains(strings.ToLower(d.Name), want) ||
			strings.Contains(strings.ToLower(d.Address), want) {
			picked = d
			break
		}
	}
	if picked == nil {
		fmt.Printf("没找到匹配 %q 的设备。已配对设备：\n", match)
		for _, d := range devs {
			fmt.Printf("  %-17s  %s\n", d.Address, d.Name)
		}
		return fmt.Errorf("未匹配到设备")
	}

	fmt.Printf("设备: %s  %s\n", picked.Address, picked.Name)

	paths := []string{}
	if p := os.Getenv("FANF9_PATH"); p != "" {
		paths = []string{p}
	} else {
		sp, err := ServicePaths(picked.Address, picked.Path)
		if err != nil {
			fmt.Printf("枚举服务节点失败: %v\n", err)
		}
		paths = append(paths, sp...)
		paths = append(paths, picked.Path) // 兜底
	}

	var (
		c          *BLEConn
		writeCand  *gattCharacteristic
		notifyCand *gattCharacteristic
		nameChar   *gattCharacteristic
		svcs       []gattService
	)
	for _, path := range paths {
		opened, ss, wc, nc, nameC, err := tryPath(path)
		if err != nil {
			fmt.Printf("✗ %s\n    %v\n", path, err)
			continue
		}
		fmt.Printf("✓ %s\n", path)
		c, svcs, writeCand, notifyCand, nameChar = opened, ss, wc, nc, nameC
		break
	}
	if c == nil {
		return fmt.Errorf("所有接口都打不开")
	}
	defer c.Close()

	if os.Getenv("FANF9_VERBOSE") != "" {
		fmt.Printf("\n共 %d 个服务：\n", len(svcs))
		for i := range svcs {
			fmt.Printf("  服务 %s attr=0x%04x\n", svcs[i].UUID.String(), svcs[i].AttrHandle)
			chars, _ := c.getChars(&svcs[i])
			for j := range chars {
				fmt.Printf("    特征 %s attr=0x%04x val=0x%04x [%s]\n",
					chars[j].UUID.String(), chars[j].AttrHandle, chars[j].ValueHandle, propsString(chars[j]))
			}
		}
	}
	fmt.Printf("\n写特征: %s\n通知特征: %s\n\n", writeCand.UUID.String(), notifyCand.UUID.String())

	if nameChar != nil {
		fmt.Print("[1] 读 DeviceName(2a00): ")
		fmt.Println(probeRead(c, nameChar))
	}
	fmt.Print("[2] 写设置帧(flags=0x00): ")
	fmt.Println(tryWrite(c, writeCand, 0))
	fmt.Print("[3] 写设置帧(flags=0x20 无响应): ")
	fmt.Println(tryWrite(c, writeCand, 0x20))

	fmt.Print("[4] 写 CCCD: ")
	if err := c.subscribe(); err != nil {
		fmt.Println("失败:", err)
	} else {
		fmt.Println("OK")
	}

	got := make(chan string, 8)
	fmt.Print("[5] RegisterEvent: ")
	if err := registerEventRaw(c, func(b []byte) { got <- hexSpaced(b) }); err != nil {
		fmt.Println("失败:", err)
	} else {
		fmt.Println("OK")
		defer procGATTUnregisterEvent.Call(c.eventHandle, 0)
		fmt.Print("[6] 发状态轮询 ee 1a 06 … ")
		poll := StatusPoll()
		if err := c.Write(poll[:]); err != nil {
			fmt.Println("发送失败:", err)
		} else {
			fmt.Println("已发送")
		}
		select {
		case b := <-got:
			fmt.Printf("[7] 收到通知: %s\n", b)
		case <-time.After(4 * time.Second):
			fmt.Println("[7] 4 秒内没有收到通知")
		}
	}

	fmt.Print("[8] 读通知特征: ")
	fmt.Println(probeRead(c, &c.notifyChar))
	return nil
}

// tryPath 打开一个接口并挑出可写/可通知特征。
func tryPath(path string) (*BLEConn, []gattService, *gattCharacteristic, *gattCharacteristic, *gattCharacteristic, error) {
	access := envU32("FANF9_ACCESS", syscall.GENERIC_READ|syscall.GENERIC_WRITE)
	share := envU32("FANF9_SHARE", syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE)
	flags := envU32("FANF9_FLAGS", 0)

	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	h, e := syscall.CreateFile(p, access, share, nil, syscall.OPEN_EXISTING, flags, 0)
	if h == syscall.InvalidHandle {
		return nil, nil, nil, nil, nil, fmt.Errorf("CreateFile 失败: %v", e)
	}

	c := &BLEConn{handle: h, path: path, Address: parseAddress(path)}
	svcs, err := c.getServices()
	if err != nil {
		syscall.CloseHandle(h)
		return nil, nil, nil, nil, nil, fmt.Errorf("GetServices 失败: %v", err)
	}

	var writeCand, notifyCand, nameChar *gattCharacteristic
	bestWrite, bestNotify := -1, -1
	for i := range svcs {
		chars, err := c.getChars(&svcs[i])
		if err != nil {
			continue
		}
		for j := range chars {
			ch := &chars[j]
			score := 0
			if !svcs[i].UUID.IsShort {
				score += 100
			}
			if !ch.UUID.IsShort {
				score += 20
			}
			if ch.IsNotifiable != 0 && score > bestNotify {
				notifyCand, bestNotify = ch, score
			}
			if (ch.IsWritable != 0 || ch.IsWriteNoResp != 0) && score > bestWrite {
				writeCand, bestWrite = ch, score
			}
			if strings.HasPrefix(ch.UUID.String(), "00002a00-") {
				nameChar = ch
			}
		}
	}
	if writeCand == nil || notifyCand == nil {
		syscall.CloseHandle(h)
		return nil, nil, nil, nil, nil, fmt.Errorf("没有可写/可通知特征（write=%v notify=%v）",
			writeCand != nil, notifyCand != nil)
	}
	c.writeChar = *writeCand
	c.notifyChar = *notifyCand
	return c, svcs, writeCand, notifyCand, nameChar, nil
}

// probeRead 走官方式两段式（先问长度再取数据），打印每次 HRESULT。
func probeRead(c *BLEConn, ch *gattCharacteristic) string {
	var need uint16
	hr, _, _ := procGATTGetCharValue.Call(uintptr(c.handle), uintptr(unsafe.Pointer(ch)),
		0, 0, uintptr(unsafe.Pointer(&need)), 0)
	if msg := hrMsg(hr); msg != "" {
		return fmt.Sprintf("probe=%s need=%d", msg, need)
	}
	buf := make([]byte, 8+need)
	*(*uint32)(unsafe.Pointer(&buf[0])) = uint32(need)
	hr, _, _ = procGATTGetCharValue.Call(uintptr(c.handle), uintptr(unsafe.Pointer(ch)),
		uintptr(need), uintptr(unsafe.Pointer(&buf[0])), 0, 0)
	if msg := hrMsg(hr); msg != "" {
		return "get=" + msg
	}
	n := int(*(*uint32)(unsafe.Pointer(&buf[0])))
	return fmt.Sprintf("OK %s", hexSpaced(buf[4:4+n]))
}

// tryWrite 用一个无害的状态轮询帧试写，打印 HRESULT。
func tryWrite(c *BLEConn, ch *gattCharacteristic, flags uint32) string {
	f := StatusPoll()
	buf := make([]byte, 4+len(f))
	*(*uint32)(unsafe.Pointer(&buf[0])) = uint32(len(f))
	copy(buf[4:], f[:])
	hr, _, _ := procGATTSetCharValue.Call(uintptr(c.handle),
		uintptr(unsafe.Pointer(ch)),
		uintptr(unsafe.Pointer(&buf[0])), 0, uintptr(flags))
	if msg := hrMsg(hr); msg != "" {
		return msg
	}
	return "OK"
}

// hrMsg 把 HRESULT 转成可读串；成功返回空串。
func hrMsg(hr uintptr) string {
	v := int32(hr)
	if v >= 0 {
		return ""
	}
	return fmt.Sprintf("HRESULT 0x%08x（Win32 %d）", uint32(v), uint32(v)&0xffff)
}

// registerEventRaw 注册通知回调。
func registerEventRaw(c *BLEConn, sink func([]byte)) error {
	reg := gattValueChangedRegistration{NumCharacteristics: 1, Char: c.notifyChar}
	c.callbackPtr = syscall.NewCallback(func(eventType uint32, out unsafe.Pointer, ctx unsafe.Pointer) uintptr {
		if out == nil {
			return 0
		}
		ev := (*gattValueChangedEvent)(out)
		if ev.Value == nil || ev.Value.DataSize == 0 || ev.Value.DataSize > 512 {
			return 0
		}
		src := unsafe.Slice((*byte)(unsafe.Pointer(&ev.Value.Data[0])), int(ev.Value.DataSize))
		sink(append([]byte(nil), src...))
		return 0
	})
	var handle uintptr
	hr, _, _ := procGATTRegisterEvent.Call(uintptr(c.handle), eventCharacteristicChange,
		uintptr(unsafe.Pointer(&reg)), c.callbackPtr, 0,
		uintptr(unsafe.Pointer(&handle)), 0)
	if err := hrErr("BluetoothGATTRegisterEvent", hr); err != nil {
		return err
	}
	c.eventHandle = handle
	return nil
}
