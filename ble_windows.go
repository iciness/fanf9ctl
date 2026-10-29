//go:build windows

package main

// Win32 Bluetooth GATT（bluetoothapis.dll）封装。
//
// 结构与常量严格对照 Windows SDK 10.0.26100 的 bthledef.h，保证二进制布局一致：
//
//	BTH_LE_UUID                       20 字节（BOOLEAN + 4 字节对齐的 16 字节 union）
//	BTH_LE_GATT_SERVICE               24
//	BTH_LE_GATT_CHARACTERISTIC        36
//	BTH_LE_GATT_DESCRIPTOR            32
//	BTH_LE_GATT_CHARACTERISTIC_VALUE  8 + 数据
//	BTH_LE_GATT_DESCRIPTOR_VALUE      80
//
// 之所以不用 cgo，是为了让成品在测试机上 `go build` 直接出单个 exe。

import (
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// ---------- DLL / 过程 ----------

var (
	modBluetooth = syscall.NewLazyDLL("bluetoothapis.dll")
	modSetupAPI  = syscall.NewLazyDLL("setupapi.dll")
	modAdvapi32  = syscall.NewLazyDLL("advapi32.dll")

	procGATTGetServices     = modBluetooth.NewProc("BluetoothGATTGetServices")
	procGATTGetChars        = modBluetooth.NewProc("BluetoothGATTGetCharacteristics")
	procGATTGetDescriptors  = modBluetooth.NewProc("BluetoothGATTGetDescriptors")
	procGATTSetCharValue    = modBluetooth.NewProc("BluetoothGATTSetCharacteristicValue")
	procGATTGetCharValue    = modBluetooth.NewProc("BluetoothGATTGetCharacteristicValue")
	procGATTSetDescriptor   = modBluetooth.NewProc("BluetoothGATTSetDescriptorValue")
	procGATTRegisterEvent   = modBluetooth.NewProc("BluetoothGATTRegisterEvent")
	procGATTUnregisterEvent = modBluetooth.NewProc("BluetoothGATTUnregisterEvent")

	procSetupDiGetClassDevs          = modSetupAPI.NewProc("SetupDiGetClassDevsW")
	procSetupDiEnumDeviceInterfaces  = modSetupAPI.NewProc("SetupDiEnumDeviceInterfaces")
	procSetupDiGetInterfaceDetail    = modSetupAPI.NewProc("SetupDiGetDeviceInterfaceDetailW")
	procSetupDiDestroyDeviceInfoList = modSetupAPI.NewProc("SetupDiDestroyDeviceInfoList")

	procRegOpenKeyExW   = modAdvapi32.NewProc("RegOpenKeyExW")
	procRegQueryValueEx = modAdvapi32.NewProc("RegQueryValueExW")
	procRegCloseKey     = modAdvapi32.NewProc("RegCloseKey")
)

// ---------- Win32 结构 ----------

type winGUID struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

// GUID_BLUETOOTHLE_DEVICE_INTERFACE = {781aee18-7733-4ce4-add0-91f41c67b592}
var guidBLEDeviceInterface = winGUID{
	Data1: 0x781aee18, Data2: 0x7733, Data3: 0x4ce4,
	Data4: [8]byte{0xad, 0xd0, 0x91, 0xf4, 0x1c, 0x67, 0xb5, 0x92},
}

type bthLeUUID struct {
	IsShort bool
	_       [3]byte
	Value   [16]byte // union：短 UUID 放前 2 字节，长 UUID 是 GUID
}

func (u bthLeUUID) String() string {
	if u.IsShort {
		v := uint16(u.Value[0]) | uint16(u.Value[1])<<8
		return fmt.Sprintf("0000%04x-0000-1000-8000-00805f9b34fb", v)
	}
	return fmt.Sprintf("%08x-%04x-%04x-%s-%s",
		le32(u.Value[0:4]), le16(u.Value[4:6]), le16(u.Value[6:8]),
		hex.EncodeToString(u.Value[8:10]), hex.EncodeToString(u.Value[10:16]))
}

func le16(b []byte) uint16 { return uint16(b[0]) | uint16(b[1])<<8 }
func le32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

// GUID 把 BTH_LE_UUID 还原成设备接口用的 GUID。
// 短 UUID（0x1800 之类）按蓝牙基础 UUID 展开。
func (u bthLeUUID) GUID() winGUID {
	var g winGUID
	if u.IsShort {
		g.Data1 = uint32(uint16(u.Value[0]) | uint16(u.Value[1])<<8)
		g.Data3 = 0x1000
		g.Data4 = [8]byte{0x80, 0x00, 0x00, 0x80, 0x5f, 0x9b, 0x34, 0xfb}
		return g
	}
	g.Data1 = le32(u.Value[0:4])
	g.Data2 = le16(u.Value[4:6])
	g.Data3 = le16(u.Value[6:8])
	copy(g.Data4[:], u.Value[8:16])
	return g
}

type gattService struct {
	UUID       bthLeUUID
	AttrHandle uint16
	_          [2]byte
}

type gattCharacteristic struct {
	ServiceHandle uint16
	_             [2]byte
	UUID          bthLeUUID
	AttrHandle    uint16
	ValueHandle   uint16
	IsBroadcast   byte
	IsReadable    byte
	IsWritable    byte
	IsWriteNoResp byte
	IsSignedWrite byte
	IsNotifiable  byte
	IsIndicatable byte
	HasExtended   byte
}

type gattDescriptor struct {
	ServiceHandle        uint16
	CharacteristicHandle uint16
	DescriptorType       uint32
	UUID                 bthLeUUID
	AttrHandle           uint16
	_                    [2]byte
}

type gattCharValue struct {
	DataSize uint32
	Data     [1]byte
}

type gattValueChangedRegistration struct {
	NumCharacteristics uint16
	_                  [2]byte
	Char               gattCharacteristic
}

type gattValueChangedEvent struct {
	ChangedAttributeHandle uint16
	_                      [6]byte
	DataSize               uintptr
	Value                  *gattCharValue
}

type gattDescriptorValue struct {
	DescriptorType uint32
	UUID           bthLeUUID
	Union          [48]byte
	DataSize       uint32
	Data           [1]byte
	_              [3]byte
}

type spDeviceInterfaceData struct {
	CbSize             uint32
	InterfaceClassGUID winGUID
	Flags              uint32
	Reserved           uintptr
}

// ---------- 常量 ----------

const (
	digcfPresent         = 0x00000002
	digcfDeviceInterface = 0x00000010
	errNoMoreItems       = 259

	gattFlagNone              = 0x00000000
	gattFlagWriteWithoutResp  = 0x00000020
	eventCharacteristicChange = 0 // CharacteristicValueChangedEvent
	descriptorTypeCCCD        = 2 // ClientCharacteristicConfiguration
)

const hresultMoreData = int32(-2147024662) // 0x800700EA (ERROR_MORE_DATA)

// ---------- 错误 ----------

func hrErr(what string, hr uintptr) error {
	v := int32(hr)
	if v >= 0 || v == hresultMoreData {
		return nil
	}
	return fmt.Errorf("%s: HRESULT 0x%08x（Win32 %d）", what, uint32(v), uint32(v)&0xffff)
}

// ---------- 设备枚举 ----------

// ListDevices 枚举系统里 Windows 已知的所有 BLE 设备接口。
func ListDevices() ([]DeviceInfo, error) {
	guid := guidBLEDeviceInterface
	h, _, err := procSetupDiGetClassDevs.Call(
		uintptr(unsafe.Pointer(&guid)), 0, 0, digcfPresent|digcfDeviceInterface)
	if h == uintptr(syscall.InvalidHandle) || h == 0 {
		return nil, fmt.Errorf("SetupDiGetClassDevsW 失败: %v", err)
	}
	defer procSetupDiDestroyDeviceInfoList.Call(h)

	var out []DeviceInfo
	seen := map[string]bool{}
	for i := uint32(0); ; i++ {
		var did spDeviceInterfaceData
		did.CbSize = uint32(unsafe.Sizeof(did))
		ok, _, _ := procSetupDiEnumDeviceInterfaces.Call(h, 0, uintptr(unsafe.Pointer(&guid)), uintptr(i), uintptr(unsafe.Pointer(&did)))
		if ok == 0 {
			break // ERROR_NO_MORE_ITEMS
		}
		path, err := interfaceDetail(h, &did)
		if err != nil {
			continue
		}
		addr := parseAddress(path)
		// 同一个设备可能有多个接口实例，按地址去重
		if addr == "" || seen[addr] {
			continue
		}
		seen[addr] = true
		out = append(out, DeviceInfo{
			Address: addr,
			Name:    friendlyName(addr),
			Path:    path,
		})
	}
	return out, nil
}

// ServicePaths 返回该设备每个 GATT 服务对应的"服务节点"接口路径。
//
// 这一点是实测踩出来的：\\?\BTHLE\Dev_<地址> 那个总接口只能读到缓存的
// GATT 数据库（服务/特征/描述符都拿得到），但任何真正走空中的操作——
// 读值、写值、写 CCCD、注册通知——都会失败并返回
// ERROR_INVALID_FUNCTION(0x80070001)。必须改开按服务拆分的
// \\?\BTHLEDevice\{服务UUID}_Dev_...<地址> 节点，这些调用才成立。
//
// 厂商自定义服务排在前面，先试。
func ServicePaths(addr, basePath string) ([]string, error) {
	p, err := syscall.UTF16PtrFromString(basePath)
	if err != nil {
		return nil, err
	}
	h, e := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, 0, 0)
	if h == syscall.InvalidHandle {
		return nil, fmt.Errorf("CreateFile(%s) 失败: %v", basePath, e)
	}
	defer syscall.CloseHandle(h)

	c := &BLEConn{handle: h}
	svcs, err := c.getServices()
	if err != nil {
		return nil, err
	}

	token := strings.ToLower(strings.ReplaceAll(addr, ":", ""))
	type cand struct {
		path string
		rank int
	}
	var cands []cand
	for _, s := range svcs {
		g := s.UUID.GUID()
		hdi, _, _ := procSetupDiGetClassDevs.Call(
			uintptr(unsafe.Pointer(&g)), 0, 0, digcfPresent|digcfDeviceInterface)
		if hdi == uintptr(syscall.InvalidHandle) || hdi == 0 {
			continue
		}
		for i := uint32(0); ; i++ {
			var did spDeviceInterfaceData
			did.CbSize = uint32(unsafe.Sizeof(did))
			ok, _, _ := procSetupDiEnumDeviceInterfaces.Call(hdi, 0,
				uintptr(unsafe.Pointer(&g)), uintptr(i), uintptr(unsafe.Pointer(&did)))
			if ok == 0 {
				break
			}
			path, err := interfaceDetail(hdi, &did)
			if err != nil || !strings.Contains(strings.ToLower(path), token) {
				continue
			}
			rank := 1
			if !s.UUID.IsShort {
				rank = 0
			}
			cands = append(cands, cand{path: path, rank: rank})
		}
		procSetupDiDestroyDeviceInfoList.Call(hdi)
	}

	sort.SliceStable(cands, func(i, j int) bool { return cands[i].rank < cands[j].rank })
	paths := make([]string, 0, len(cands))
	for _, c := range cands {
		paths = append(paths, c.path)
	}
	return paths, nil
}

func interfaceDetail(h uintptr, did *spDeviceInterfaceData) (string, error) {
	// SP_DEVICE_INTERFACE_DETAIL_DATA_W：32 位 cbSize=6，64 位 cbSize=8
	cbSize := uint32(6)
	if unsafe.Sizeof(uintptr(0)) == 8 {
		cbSize = 8
	}
	buf := make([]byte, cbSize+2048)
	*(*uint32)(unsafe.Pointer(&buf[0])) = cbSize

	var needed uint32
	ok, _, err := procSetupDiGetInterfaceDetail.Call(h, uintptr(unsafe.Pointer(did)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&needed)), 0)
	if ok == 0 {
		return "", fmt.Errorf("SetupDiGetDeviceInterfaceDetailW: %v", err)
	}
	// 路径紧跟在 DWORD cbSize 之后（结构体对齐后 DevicePath 的偏移恒为 4）
	u16 := unsafe.Slice((*uint16)(unsafe.Pointer(&buf[4])), (len(buf)-4)/2)
	n := 0
	for n < len(u16) && u16[n] != 0 {
		n++
	}
	return syscall.UTF16ToString(u16[:n]), nil
}

// parseAddress 从 \\?\BTHLEDEVICE#{...}_Dev_001122334455_Listen 之类的路径里取地址。
func parseAddress(path string) string {
	lower := strings.ToLower(path)
	// 两种路径形态：
	//   \\?\bthle#dev_d103d164fc42#...                       （总接口，地址紧跟在 dev_ 后）
	//   \\?\bthledevice#{svc}_dev_vid&0217ef_pid&f00d_..._d103d164fc42#...（服务节点）
	// 后者要往后找那段 12 位的十六进制。
	for _, marker := range []string{"_dev_", "dev_"} {
		i := strings.Index(lower, marker)
		if i < 0 {
			continue
		}
		rest := lower[i+len(marker):]
		if len(rest) >= 12 && isHex(rest[:12]) {
			return formatAddr(rest[:12])
		}
		for _, tok := range strings.FieldsFunc(rest, func(r rune) bool {
			return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f')
		}) {
			if len(tok) == 12 && isHex(tok) {
				return formatAddr(tok)
			}
		}
	}
	return ""
}

func formatAddr(hexs string) string {
	var b strings.Builder
	for j := 0; j < len(hexs); j += 2 {
		if b.Len() > 0 {
			b.WriteByte(':')
		}
		b.WriteString(hexs[j : j+2])
	}
	return b.String()
}

func parseService(path string) string {
	if i := strings.Index(path, "#{"); i >= 0 {
		if j := strings.Index(path[i+2:], "}"); j > 0 {
			return strings.ToLower(path[i+2 : i+2+j])
		}
	}
	return ""
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

// friendlyName 从注册表 BTHPORT\Parameters\Devices\<addr>\Name 读友好名。
func friendlyName(addr string) string {
	key := strings.ReplaceAll(addr, ":", "")
	if key == "" {
		return ""
	}
	sub := `SYSTEM\CurrentControlSet\Services\BTHPORT\Parameters\Devices\` + key
	p, err := syscall.UTF16PtrFromString(sub)
	if err != nil {
		return ""
	}
	var hKey syscall.Handle
	if r, _, _ := procRegOpenKeyExW.Call(0x80000002, uintptr(unsafe.Pointer(p)), 0, 0x20019, uintptr(unsafe.Pointer(&hKey))); int32(r) != 0 {
		return ""
	}
	defer procRegCloseKey.Call(uintptr(hKey))

	n, err := syscall.UTF16PtrFromString("Name")
	if err != nil {
		return ""
	}
	buf := make([]byte, 512)
	size := uint32(len(buf))
	var typ uint32
	if r, _, _ := procRegQueryValueEx.Call(uintptr(hKey), uintptr(unsafe.Pointer(n)), 0,
		uintptr(unsafe.Pointer(&typ)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size))); int32(r) != 0 {
		return ""
	}
	if typ == 1 { // REG_SZ
		u16 := unsafe.Slice((*uint16)(unsafe.Pointer(&buf[0])), int(size)/2)
		k := 0
		for k < len(u16) && u16[k] != 0 {
			k++
		}
		return syscall.UTF16ToString(u16[:k])
	}
	if typ == 3 { // REG_BINARY：Windows 把蓝牙名字按 ANSI/UTF-16 原始字节存这里
		raw := buf[:size]
		for len(raw) > 0 && raw[len(raw)-1] == 0 {
			raw = raw[:len(raw)-1]
		}
		if len(raw) == 0 {
			return ""
		}
		utf16 := len(raw)%2 == 0
		if utf16 {
			for i := 1; i < len(raw); i += 2 {
				if raw[i] != 0 {
					utf16 = false
					break
				}
			}
		}
		if utf16 {
			u16 := unsafe.Slice((*uint16)(unsafe.Pointer(&raw[0])), len(raw)/2)
			return syscall.UTF16ToString(u16)
		}
		return strings.TrimRight(string(raw), "\x00")
	}
	return ""
}

// ---------- 连接 ----------

// BLEConn 是一条已经订阅好的 GATT 连接。
type BLEConn struct {
	handle     syscall.Handle
	path       string
	Address    string
	writeChar  gattCharacteristic
	notifyChar gattCharacteristic
	noResp     bool

	eventHandle uintptr
	callbackPtr uintptr

	mu      sync.Mutex
	inbox   chan []byte
	done    chan struct{}
	once    sync.Once
	lastErr error

	Chars []CharInfo
}

// Open 打开设备接口、枚举服务/特征、订阅通知。
func Open(path, preferWrite, preferNotify string, noResp bool) (*BLEConn, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, e := syscall.CreateFile(p,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE,
		nil, syscall.OPEN_EXISTING, 0, 0)
	if h == syscall.InvalidHandle {
		return nil, fmt.Errorf("CreateFile(%s) 失败: %v", path, e)
	}

	c := &BLEConn{
		handle:  h,
		path:    path,
		Address: parseAddress(path),
		inbox:   make(chan []byte, 64),
		done:    make(chan struct{}),
	}
	if err := c.discover(preferWrite, preferNotify, noResp); err != nil {
		syscall.CloseHandle(h)
		return nil, err
	}
	if err := c.subscribe(); err != nil {
		// 订阅失败不致命：某些固件靠 RegisterEvent 就够
		c.lastErr = err
	}
	if err := c.registerEvent(); err != nil {
		syscall.CloseHandle(h)
		return nil, err
	}
	return c, nil
}

func (c *BLEConn) getServices() ([]gattService, error) {
	var count uint16
	hr, _, _ := procGATTGetServices.Call(uintptr(c.handle), 0, 0, uintptr(unsafe.Pointer(&count)), 0)
	if err := hrErr("BluetoothGATTGetServices(count)", hr); err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, nil
	}
	buf := make([]gattService, count)
	hr, _, _ = procGATTGetServices.Call(uintptr(c.handle), uintptr(count),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&count)), 0)
	if err := hrErr("BluetoothGATTGetServices(data)", hr); err != nil {
		return nil, err
	}
	return buf[:count], nil
}

func (c *BLEConn) getChars(svc *gattService) ([]gattCharacteristic, error) {
	var count uint16
	hr, _, _ := procGATTGetChars.Call(uintptr(c.handle), uintptr(unsafe.Pointer(svc)), 0, 0,
		uintptr(unsafe.Pointer(&count)), 0)
	if err := hrErr("BluetoothGATTGetCharacteristics(count)", hr); err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, nil
	}
	buf := make([]gattCharacteristic, count)
	hr, _, _ = procGATTGetChars.Call(uintptr(c.handle), uintptr(unsafe.Pointer(svc)), uintptr(count),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&count)), 0)
	if err := hrErr("BluetoothGATTGetCharacteristics(data)", hr); err != nil {
		return nil, err
	}
	return buf[:count], nil
}

func (c *BLEConn) getDescriptors(ch *gattCharacteristic) ([]gattDescriptor, error) {
	var count uint16
	hr, _, _ := procGATTGetDescriptors.Call(uintptr(c.handle), uintptr(unsafe.Pointer(ch)), 0, 0,
		uintptr(unsafe.Pointer(&count)), 0)
	if err := hrErr("BluetoothGATTGetDescriptors(count)", hr); err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, nil
	}
	buf := make([]gattDescriptor, count)
	hr, _, _ = procGATTGetDescriptors.Call(uintptr(c.handle), uintptr(unsafe.Pointer(ch)), uintptr(count),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&count)), 0)
	if err := hrErr("BluetoothGATTGetDescriptors(data)", hr); err != nil {
		return nil, err
	}
	return buf[:count], nil
}

// discover 遍历所有服务/特征，挑出可写和可通知的两个。
func (c *BLEConn) discover(preferWrite, preferNotify string, noResp bool) error {
	svcs, err := c.getServices()
	if err != nil {
		return err
	}
	if len(svcs) == 0 {
		return errors.New("设备没有任何 GATT 服务")
	}

	type cand struct {
		ch    gattCharacteristic
		svc   string
		score int
	}
	var writes, notifies []cand

	for i := range svcs {
		svc := svcs[i]
		svcUUID := svc.UUID.String()
		chars, err := c.getChars(&svcs[i])
		if err != nil {
			continue
		}
		for _, ch := range chars {
			props := propsString(ch)
			c.Chars = append(c.Chars, CharInfo{Service: svcUUID, UUID: ch.UUID.String(), Props: props,
				Notified: ch.IsNotifiable != 0, Writable: ch.IsWritable != 0 || ch.IsWriteNoResp != 0})
			score := 0
			if !svc.UUID.IsShort {
				score += 100 // 厂商自定义服务优先
			}
			if !ch.UUID.IsShort {
				score += 20
			}
			if preferWrite != "" && strings.EqualFold(ch.UUID.String(), preferWrite) {
				score += 1000
			}
			if preferNotify != "" && strings.EqualFold(ch.UUID.String(), preferNotify) {
				score += 1000
			}
			if ch.IsWritable != 0 || ch.IsWriteNoResp != 0 {
				writes = append(writes, cand{ch: ch, svc: svcUUID, score: score})
			}
			if ch.IsNotifiable != 0 {
				notifies = append(notifies, cand{ch: ch, svc: svcUUID, score: score})
			}
		}
	}

	if len(writes) == 0 {
		return errors.New("没有找到可写特征")
	}
	best := writes[0]
	for _, w := range writes[1:] {
		if w.score > best.score {
			best = w
		}
	}
	c.writeChar = best.ch
	c.noResp = noResp || best.ch.IsWritable == 0 && best.ch.IsWriteNoResp != 0

	if len(notifies) == 0 {
		c.notifyChar = gattCharacteristic{}
		return nil
	}
	bn := notifies[0]
	sameService := false
	for _, n := range notifies {
		if n.score > bn.score {
			bn = n
		}
		if n.svc == best.svc {
			sameService = true
		}
	}
	// 优先和写特征同一个服务
	if sameService {
		for _, n := range notifies {
			if n.svc == best.svc {
				bn = n
				break
			}
		}
	}
	c.notifyChar = bn.ch
	return nil
}

func propsString(ch gattCharacteristic) string {
	var p []string
	if ch.IsReadable != 0 {
		p = append(p, "read")
	}
	if ch.IsWritable != 0 {
		p = append(p, "write")
	}
	if ch.IsWriteNoResp != 0 {
		p = append(p, "write-noresp")
	}
	if ch.IsNotifiable != 0 {
		p = append(p, "notify")
	}
	if ch.IsIndicatable != 0 {
		p = append(p, "indicate")
	}
	return strings.Join(p, ",")
}

func (c *BLEConn) subscribe() error {
	if c.notifyChar.AttrHandle == 0 {
		return errors.New("没有可通知特征")
	}
	descs, err := c.getDescriptors(&c.notifyChar)
	if err != nil {
		return err
	}
	for i := range descs {
		if descs[i].DescriptorType != descriptorTypeCCCD {
			continue
		}
		var dv gattDescriptorValue
		dv.DescriptorType = descriptorTypeCCCD
		dv.UUID = descs[i].UUID
		dv.Union[0] = 1 // IsSubscribeToNotification
		dv.Union[1] = 0 // IsSubscribeToIndication
		dv.DataSize = 2
		hr, _, _ := procGATTSetDescriptor.Call(uintptr(c.handle), uintptr(unsafe.Pointer(&descs[i])),
			uintptr(unsafe.Pointer(&dv)), 0)
		return hrErr("BluetoothGATTSetDescriptorValue(CCCD)", hr)
	}
	return errors.New("没有找到 CCCD 描述符")
}

func (c *BLEConn) registerEvent() error {
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
		buf := append([]byte(nil), src...)
		select {
		case c.inbox <- buf:
		default: // 丢弃，避免回调阻塞驱动
		}
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

func (c *BLEConn) lastErrSet(err error) { c.mu.Lock(); c.lastErr = err; c.mu.Unlock() }

// Read 读一次特征值（诊断与"读回当前配置"用）。
func (c *BLEConn) Read(ch *gattCharacteristic) ([]byte, error) {
	buf := make([]byte, 4+512)
	*(*uint32)(unsafe.Pointer(&buf[0])) = 512
	var need uint16
	hr, _, _ := procGATTGetCharValue.Call(uintptr(c.handle), uintptr(unsafe.Pointer(ch)),
		uintptr(512), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&need)), 0)
	if err := hrErr("BluetoothGATTGetCharacteristicValue", hr); err != nil {
		return nil, err
	}
	n := int(*(*uint32)(unsafe.Pointer(&buf[0])))
	if n > 512 {
		n = 512
	}
	return append([]byte(nil), buf[4:4+n]...), nil
}

// Write 发送一帧。
func (c *BLEConn) Write(b []byte) error {
	buf := make([]byte, 4+len(b))
	*(*uint32)(unsafe.Pointer(&buf[0])) = uint32(len(b))
	copy(buf[4:], b)

	flags := uintptr(gattFlagNone)
	if c.noResp {
		flags = gattFlagWriteWithoutResp
	}
	hr, _, _ := procGATTSetCharValue.Call(uintptr(c.handle),
		uintptr(unsafe.Pointer(&c.writeChar)),
		uintptr(unsafe.Pointer(&buf[0])), 0, flags)
	if err := hrErr("BluetoothGATTSetCharacteristicValue", hr); err == nil {
		return nil
	}
	// 有些固件只接受 write-without-response，反过来再试一次
	alt := uintptr(gattFlagWriteWithoutResp)
	if c.noResp {
		alt = uintptr(gattFlagNone)
	}
	hr, _, _ = procGATTSetCharValue.Call(uintptr(c.handle),
		uintptr(unsafe.Pointer(&c.writeChar)),
		uintptr(unsafe.Pointer(&buf[0])), 0, alt)
	if err := hrErr("BluetoothGATTSetCharacteristicValue(alt)", hr); err != nil {
		return err
	}
	c.noResp = !c.noResp
	return nil
}

// Notifications 返回通知通道。
func (c *BLEConn) Notifications() <-chan []byte { return c.inbox }

// Close 注销事件并关闭句柄。
func (c *BLEConn) Close() error {
	c.once.Do(func() {
		if c.done != nil {
			close(c.done)
		}
	})
	if c.eventHandle != 0 {
		procGATTUnregisterEvent.Call(c.eventHandle, 0)
		c.eventHandle = 0
	}
	if c.handle != 0 && c.handle != syscall.InvalidHandle {
		syscall.CloseHandle(c.handle)
		c.handle = 0
	}
	return nil
}

func (c *BLEConn) LocalName() string { return c.Address + " @ " + c.writeChar.UUID.String() }

// WriteUUID 返回写特征的 UUID。
func (c *BLEConn) WriteUUID() string { return c.writeChar.UUID.String() }

// NotifyUUID 返回通知特征的 UUID。
func (c *BLEConn) NotifyUUID() string { return c.notifyChar.UUID.String() }

// Describe 供 UI 展示。
func (c *BLEConn) Describe() LinkInfo {
	return LinkInfo{
		Address: c.Address,
		Write:   c.WriteUUID(),
		Notify:  c.NotifyUUID(),
		Chars:   append([]CharInfo(nil), c.Chars...),
	}
}
