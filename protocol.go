package main

// 联想 风刃F9（fan_F9）蓝牙协议复刻。
//
// 结论来自对 LEGION EDGE.exe / evsBleCommunit.dll 的静态逆向，以及程序自身日志
// （%LOCALAPPDATA%\LEGION EDGE\NeoUsb.log、BleC.log）的双向印证。
//
// 线上 BLE 报文固定 20 字节：
//
//	[0]      0xEE                 帧头
//	[1]      CMD                  命令
//	[2]      SUB                  子命令
//	[3..4]   ARG16 (小端)          16 位参数
//	[5..19]  DATA                 数据
//
// 对应的 USB HID 报文是 30 字节，多出：
//
//	[0]=0x04 (Report ID)、[1..2]=sum(bytes[3..63]) 16 位累加和、[7]=保留
//
// 两者的换算关系（已用日志逐字节核对）：
//
//	ble[0]     = 0xEE
//	ble[1..4]  = usb[3..6]
//	ble[5..]   = usb[8..]        // 即丢掉 usb[1]、usb[2]、usb[7]

const (
	frameLen = 20
	frameHdr = 0xEE
)

// 命令字
const (
	cmdInfo   byte = 0x03 // 连接/初始化握手，SUB=0x22，回 AA 55 ...
	subInfo   byte = 0x22
	cmdSet    byte = 0x06 // 写灯光 + 风扇，SUB=0x0F，设备回显作为 ACK
	subSet    byte = 0x0F
	cmdRead   byte = 0x05 // 读当前灯光配置，SUB=0x0F
	subRead   byte = 0x0F
	cmdStatus byte = 0x1A // 状态轮询，SUB=0x06，回 8 字节
	subStatus byte = 0x06
)

// Frame 是 20 字节的 BLE 报文。
type Frame [frameLen]byte

// NewFrame 组装一帧。data 超过 15 字节会被截断。
func NewFrame(cmd, sub byte, arg uint16, data []byte) Frame {
	var f Frame
	f[0] = frameHdr
	f[1] = cmd
	f[2] = sub
	f[3] = byte(arg)
	f[4] = byte(arg >> 8)
	copy(f[5:], data)
	return f
}

// Hex 返回带空格的十六进制，和 LEGION EDGE 的 BleC.log 格式一致。
func (f Frame) Hex() string { return hexSpaced(f[:]) }

// StatusPoll 是每 3 秒一次的状态查询帧：ee 1a 06 00 00 + 15 个 0。
func StatusPoll() Frame { return NewFrame(cmdStatus, subStatus, 0, nil) }

// InfoQuery 是连接后的握手帧：ee 03 22 00 00 + 15 个 0。
func InfoQuery() Frame { return NewFrame(cmdInfo, subInfo, 0, nil) }

// LightRead 读取当前灯光配置：ee 05 0f 00 00 + 15 个 0。
func LightRead() Frame { return NewFrame(cmdRead, subRead, 0, nil) }

// Status 是状态回报的解析结果。
type Status struct {
	RPM   int    // 转速（原始值 / 4）
	Valid bool   // 状态字节非 0
	Light byte   // 档位字节（BLE 通道通常拿不到，只有 >=9 字节时才有效）
	Raw   []byte // 原始字节副本
}

// ParseStatus 解析通知。F9 在 BLE 上只回 8 字节：
//
//	ee 1a 06 00 00 01 <rpmLo> <rpmHi>
//
// 转速 = (rpmHi<<8 | rpmLo) / 4。
func ParseStatus(b []byte) (Status, bool) {
	if len(b) < 8 || b[0] != frameHdr || b[1] != cmdStatus || b[2] != subStatus {
		return Status{}, false
	}
	st := Status{
		Valid: b[5] != 0,
		RPM:   int(uint16(b[6])|uint16(b[7])<<8) >> 2,
		Raw:   append([]byte(nil), b...),
	}
	if len(b) > 8 {
		st.Light = b[8]
	}
	return st, true
}

// ParseEcho 判断是否为设置帧的回显 ACK。F9 会把收到的 20 字节原样回显。
func ParseEcho(sent, got []byte) bool {
	if len(got) < frameLen {
		return false
	}
	if got[0] != frameHdr || got[1] != cmdSet || got[2] != subSet {
		return false
	}
	n := len(sent)
	if len(got) < n {
		n = len(got)
	}
	for i := 0; i < n; i++ {
		if sent[i] != got[i] {
			return false
		}
	}
	return true
}

// SetParams 是"设置灯光 + 风扇"的业务参数。
//
// 实测载荷（20 字节帧的 [5..19]，2026-10-28 用官方 App 当对照组标定）：
//
//	00 03 09 02 00 00 ff ff ff 00 00 00 00 02 00
//	│  │  │  │  │  │  └─ R G B            └─ 风扇档位（0..3）
//	│  │  │  │  │  └─ 多色标志（呼吸/渐变 = 1，恒亮 = 0）
//	│  │  │  │  └─ 恒 0
//	│  │  │  └─ 恒 2（官方 2448 条帧全是 2，含义未知）
//	│  │  └─ 亮度 Light（0..9）
//	│  └─ 灯光模式 SelectItem：3=恒亮，2=呼吸，1=渐变
//	└─ Fanmode（0；切 AI 模式时的取值尚未验证）
//
// 标定依据（点官方 App、看 BleC.log 新增帧）：
//   - 点四个风扇档位 → 只有 data[13] 从 0→1→2→3 变化，转速 1126→2947 跟着变
//   - 点四个灯效     → 只有 data[1] 从 3→2→1 变化，data[5] 同步 0→1→1
//   - 拖亮度滑块     → 只有 data[2] 从 9 降到 0
//
// 之前的版本把档位写在 data[3]、把灯光模式写成 0..3，所以"档位没用"、
// 而且 data[1]=0 其实是关灯，才出现"指示灯不亮"。
type SetParams struct {
	LightMode  byte // data[1] 灯光模式：3=恒亮，2=呼吸，1=渐变
	Brightness byte // data[2] 亮度 0..9
	Speed      byte // data[13] 风扇档位 0..3
	R, G, B    byte // data[6..8]
	MultiColor byte // data[5]：呼吸/渐变置 1
	FanMode    byte // data[0]：0（AI 模式未验证）
}

// setDataLen 是设置帧的载荷长度（15 字节，正好填满 20 字节帧）。
const setDataLen = 15

// 设置帧里各字段的位置（标定过的）。
const (
	offFanMode    = 0
	offLightMode  = 1
	offBrightness = 2
	offFixed2     = 3 // 官方恒发 2
	offMultiColor = 5
	offR          = 6
	offG          = 7
	offB          = 8
	offSpeed      = 13
)

// BuildSet 按实测布局组装设置帧。
func BuildSet(p SetParams, cfg *Config) Frame {
	var d [setDataLen]byte
	d[offFanMode] = p.FanMode
	d[offLightMode] = p.LightMode
	d[offBrightness] = p.Brightness
	d[offFixed2] = 2
	d[offMultiColor] = p.MultiColor
	d[offR] = p.R
	d[offG] = p.G
	d[offB] = p.B
	d[offSpeed] = p.Speed

	// 亮度偏移仍然留成可配：万一别的固件放在别处。
	if cfg != nil && cfg.BrightnessOffset >= 0 && cfg.BrightnessOffset < setDataLen &&
		cfg.BrightnessOffset != offBrightness {
		d[offBrightness] = 0
		d[cfg.BrightnessOffset] = p.Brightness
	}
	return NewFrame(cmdSet, subSet, 0, d[:])
}

// MultiColorFor 返回该灯光模式对应的多色标志（data[5]）。
//
// 置 1 时设备走"多色"逻辑，会忽略 data[6..8] 里的单色 —— 实测在呼吸模式下
// 选颜色没反应就是这个原因。所以只有渐变（设备自己轮换颜色）才置 1，
// 恒亮和呼吸都用单色，置 0，颜色才会生效。
func MultiColorFor(lightMode byte) byte {
	if lightMode == 1 { // 1 = 渐变
		return 1
	}
	return 0
}

// LightConfig 是 05/0f 读回的设备当前配置（与设置帧同构）。
type LightConfig struct {
	FanMode    byte
	LightMode  byte
	Brightness byte
	Speed      byte
	Multi      byte
	R, G, B    byte
}

// ParseLightConfig 解析 "读当前灯光配置" 的回复。
func ParseLightConfig(b []byte) (LightConfig, bool) {
	if len(b) < frameLen || b[0] != frameHdr || b[1] != cmdRead || b[2] != subRead {
		return LightConfig{}, false
	}
	d := b[5:]
	return LightConfig{
		FanMode:    d[offFanMode],
		LightMode:  d[offLightMode],
		Brightness: d[offBrightness],
		Speed:      d[offSpeed],
		Multi:      d[offMultiColor],
		R:          d[offR],
		G:          d[offG],
		B:          d[offB],
	}, true
}
func hexSpaced(b []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, 0, len(b)*3)
	for i, v := range b {
		if i > 0 {
			out = append(out, ' ')
		}
		out = append(out, hex[v>>4], hex[v&0x0f])
	}
	return string(out)
}

func hexCompact(b []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, v := range b {
		out = append(out, hex[v>>4], hex[v&0x0f])
	}
	return string(out)
}
