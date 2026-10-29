package main

import (
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

// DeviceInfo 描述一个已配对的 BLE 设备实例（每个 GATT 服务可能出现一条）。
type DeviceInfo struct {
	Address string `json:"address"` // 蓝牙地址，如 aa:bb:cc:dd:ee:ff
	Name    string `json:"name"`    // 注册表里的友好名
	Service string `json:"service"` // 该接口对应的主服务 UUID
	Path    string `json:"path"`    // 设备接口路径
}

// CharInfo 用于在 UI 里展示发现到的服务/特征。
type CharInfo struct {
	Service  string `json:"service"`
	UUID     string `json:"uuid"`
	Props    string `json:"props"`
	Notified bool   `json:"notified"`
	Writable bool   `json:"writable"`
}

// LinkInfo 是连接的可展示信息。
type LinkInfo struct {
	Address string     `json:"address"`
	Write   string     `json:"writeChar"`
	Notify  string     `json:"notifyChar"`
	Chars   []CharInfo `json:"chars"`
}

// link 抽象"一条可用的设备链路"，便于在没有硬件时用模拟器跑通 UI。
type link interface {
	Write([]byte) error
	Notifications() <-chan []byte
	Close() error
	Describe() LinkInfo
}

// LogEntry 是界面上的一行日志。
type LogEntry struct {
	T    string `json:"t"`
	Dir  string `json:"dir"` // TX / RX / SYS / ERR
	Data string `json:"data"`
	Note string `json:"note"`
}

// Settings 是当前要下发的业务参数。
type Settings struct {
	FanAI      bool     `json:"fanAI"`      // true=AI模式, false=固定转速
	Speed      int      `json:"speed"`      // 0..3 安静/平衡/野兽/超能
	LightMode  int      `json:"lightMode"`  // 0..3 恒亮/呼吸/渐变/动态光效
	Brightness int      `json:"brightness"` // 1..9
	Colors     []string `json:"colors"`     // "#RRGGBB"
	ColorSel   int      `json:"colorSel"`
}

func defaultSettings() Settings {
	return Settings{
		FanAI:      false,
		Speed:      1,
		LightMode:  0,
		Brightness: 9,
		Colors:     []string{"#ffffff", "#ff0000", "#00ff00", "#0000ff"},
		ColorSel:   0,
	}
}

// Snapshot 是给前端轮询的完整状态。
type Snapshot struct {
	Connected  bool         `json:"connected"`
	Simulated  bool         `json:"simulated"`
	Devices    []DeviceInfo `json:"devices"`
	Link       LinkInfo     `json:"link"`
	RPM        int          `json:"rpm"`
	Status     string       `json:"status"`
	LastError  string       `json:"lastError"`
	Settings   Settings     `json:"settings"`
	LastSetHex string       `json:"lastSetHex"`
	CPU        int          `json:"cpu"`
	Logs       []LogEntry   `json:"logs"`
	Config     *Config      `json:"config"`
}

// Controller 串起协议、链路和 UI 状态。
type Controller struct {
	cfg *Config
	sim bool

	mu       sync.Mutex
	lk       link
	status   string
	lastErr  string
	rpm      int
	settings Settings
	logs     []LogEntry
	lastSet  Frame
	stopCh   chan struct{}

	// 「自动」模式：按 CPU 占用率换档，连续两拍一致才切
	loadHist    []int
	autoPending int
	autoCount   int

	// 上次保存的设置，用来避免重复写盘；restored 表示启动时恢复自文件
	lastSaved Settings
	restored  bool
	statePath string

	devices []DeviceInfo
}

// NewController 创建控制器。
// NewController 创建控制器。initial 是上次保存的设置（没有就传 defaultSettings）。
// restored 为 true 表示这是用户上次的选择，连上后要主动下发、而不是按设备现状覆盖。
func NewController(cfg *Config, sim bool, initial Settings, restored bool) *Controller {
	return &Controller{
		cfg:       cfg,
		sim:       sim,
		settings:  initial,
		lastSaved: initial,
		restored:  restored,
		logs:      make([]LogEntry, 0, 256),
		status:    "未连接",
	}
}

func (c *Controller) addLog(dir, data, note string) {
	c.logs = append(c.logs, LogEntry{
		T:    time.Now().Format("15:04:05.000"),
		Dir:  dir,
		Data: data,
		Note: note,
	})
	if len(c.logs) > 400 {
		c.logs = c.logs[len(c.logs)-400:]
	}
}

// Snapshot 生成前端状态。
func (c *Controller) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	s := Settings{
		FanAI:      c.settings.FanAI,
		Speed:      c.settings.Speed,
		LightMode:  c.settings.LightMode,
		Brightness: c.settings.Brightness,
		Colors:     append([]string(nil), c.settings.Colors...),
		ColorSel:   c.settings.ColorSel,
	}
	snap := Snapshot{
		Connected: c.lk != nil,
		Simulated: c.sim,
		Devices:   append([]DeviceInfo(nil), c.devices...),
		RPM:       c.rpm,
		Status:    c.status,
		LastError: c.lastErr,
		CPU:       cpuLoad(),
		Settings:  s,
		Logs:      append([]LogEntry(nil), c.logs...),
		Config:    c.cfg,
	}
	if c.lk != nil {
		snap.Link = c.lk.Describe()
	}
	if c.lastSet[0] != 0 {
		snap.LastSetHex = c.lastSet.Hex()
	}
	return snap
}

// Connect 按设备名/地址匹配并建立链路。
func (c *Controller) Connect(match string) error {
	c.mu.Lock()
	if c.lk != nil {
		c.mu.Unlock()
		return fmt.Errorf("已经连接")
	}
	sim := c.sim
	c.status = "正在连接…"
	c.mu.Unlock()

	if sim {
		lk := newSimLink()
		c.mu.Lock()
		c.lk = lk
		c.status = "已连接（模拟设备）"
		c.addLog("SYS", "sim", "模拟链路已建立")
		c.mu.Unlock()
		go c.readLoop(lk)
		go c.loop(lk)
		return nil
	}

	devs, err := ListDevices()
	if err != nil {
		c.fail("枚举蓝牙设备失败: " + err.Error())
		return err
	}
	c.mu.Lock()
	c.devices = devs
	c.mu.Unlock()

	if len(devs) == 0 {
		err := fmt.Errorf("系统里没有已配对的 BLE 设备。请先在 Windows 设置里配对散热器")
		c.fail(err.Error())
		return err
	}

	// 优先名字匹配，其次地址匹配，最后退化为第一个
	var picked *DeviceInfo
	want := strings.ToLower(strings.TrimSpace(match))
	if want == "" {
		want = strings.ToLower(c.cfg.DeviceName)
	}
	for i := range devs {
		d := &devs[i]
		if want != "" && (strings.EqualFold(d.Name, want) || strings.EqualFold(shortAddr(d.Address), want) ||
			strings.EqualFold(d.Address, want) || strings.Contains(strings.ToLower(d.Name), want)) {
			picked = d
			break
		}
	}
	if picked == nil && len(devs) == 1 {
		picked = &devs[0]
	}
	if picked == nil {
		err := fmt.Errorf("没找到匹配 %q 的设备（共发现 %d 个接口）", match, len(devs))
		c.fail(err.Error())
		return err
	}

	// 打开"服务节点"接口（见 ble_windows.go 里 ServicePaths 的说明）。
	// 只开总接口的话，枚举能成但读写全是 ERROR_INVALID_FUNCTION。
	paths, perr := ServicePaths(picked.Address, picked.Path)
	if perr != nil || len(paths) == 0 {
		c.addLog("ERR", "", "枚举服务节点失败，退回总接口: "+errString(perr))
		paths = []string{picked.Path}
	}

	var conn *BLEConn
	var lastErr error
	for _, path := range paths {
		c.addLog("SYS", picked.Name, "打开 "+path)
		opened, err := Open(path, c.cfg.WriteCharUUID, c.cfg.NotifyCharUUID, c.cfg.WriteWithoutResponse)
		if err == nil {
			if opened.Address == "" {
				opened.Address = picked.Address
			}
			conn = opened
			break
		}
		lastErr = err
		c.addLog("ERR", "", "该接口不可用: "+err.Error())
	}
	if conn == nil {
		if lastErr == nil {
			lastErr = fmt.Errorf("没有可用的 GATT 接口")
		}
		c.fail("打开 GATT 失败: " + lastErr.Error())
		return lastErr
	}

	c.mu.Lock()
	c.lk = conn
	c.status = "已连接"
	c.addLog("SYS", conn.Address, fmt.Sprintf("写=%s 通知=%s", conn.WriteUUID(), conn.NotifyUUID()))
	c.mu.Unlock()

	go c.readLoop(conn)
	go c.loop(conn)

	// 握手 + 读一次灯光配置
	c.send(conn, InfoQuery(), "握手 03/22")
	time.Sleep(150 * time.Millisecond)
	c.send(conn, LightRead(), "读取灯光配置 05/0f")
	time.Sleep(150 * time.Millisecond)

	// 有本地保存的设置就按它下发（同时把设备里可能残留的 data[0]=1 清掉）；
	// 没有就把刚读回来的现状原样发一帧，同样清残留。
	time.Sleep(250 * time.Millisecond)
	c.mu.Lock()
	cur := c.settings
	restored := c.restored
	c.mu.Unlock()
	label := "按本地设置下发 06/0f"
	if !restored {
		label = "回写现状 06/0f"
	}
	if f := c.buildFrame(cur); true {
		if err := c.send(conn, f, label); err == nil {
			c.mu.Lock()
			c.lastSet = f
			c.mu.Unlock()
		}
	}
	return nil
}

func shortAddr(a string) string { return strings.ReplaceAll(a, ":", "") }

func (c *Controller) fail(msg string) {
	c.mu.Lock()
	c.status = "未连接"
	c.lastErr = msg
	c.addLog("ERR", "", msg)
	c.mu.Unlock()
}

// Disconnect 断开。
func (c *Controller) Disconnect() {
	c.mu.Lock()
	lk := c.lk
	c.lk = nil
	c.status = "未连接"
	c.rpm = 0
	if c.stopCh != nil {
		close(c.stopCh)
		c.stopCh = nil
	}
	c.addLog("SYS", "", "断开连接")
	c.mu.Unlock()
	if lk != nil {
		lk.Close()
	}
}

// send 发一帧并记录日志。
func (c *Controller) send(lk link, f Frame, note string) error {
	if err := lk.Write(f[:]); err != nil {
		c.mu.Lock()
		c.lastErr = err.Error()
		c.addLog("ERR", f.Hex(), note+" 失败: "+err.Error())
		c.mu.Unlock()
		return err
	}
	c.mu.Lock()
	c.addLog("TX", f.Hex(), note)
	c.mu.Unlock()
	return nil
}

// readLoop 消费通知。
func (c *Controller) readLoop(lk link) {
	done := c.doneCh()
	for {
		select {
		case b, ok := <-lk.Notifications():
			if !ok {
				return
			}
			c.handleNotify(b)
		case <-done:
			return
		}
	}
}

// doneCh 返回当前会话的停止信号。
func (c *Controller) doneCh() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopCh == nil {
		c.stopCh = make(chan struct{})
	}
	return c.stopCh
}

func (c *Controller) handleNotify(b []byte) {
	note := "通知"
	if st, ok := ParseStatus(b); ok {
		c.mu.Lock()
		c.rpm = st.RPM
		c.addLog("RX", hexSpaced(b), fmt.Sprintf("状态 rpm=%d", st.RPM))
		c.mu.Unlock()
		return
	}
	// 05/0f 的回复：设备当前配置。
	// 只有"没有本地保存的设置"时才拿它来初始化界面；否则本地设置优先，
	// 免得重启后被设备里残留的旧字节（比如 data[0]=1 的锁档状态）带跑。
	if lc, ok := ParseLightConfig(b); ok {
		c.mu.Lock()
		if !c.restored {
			if i := indexOf(c.cfg.LightModes, lc.LightMode); i >= 0 {
				c.settings.LightMode = i
			}
			if i := indexOf(c.cfg.Speeds, lc.Speed); i >= 0 {
				c.settings.Speed = i
			}
			if lc.Brightness <= 9 {
				c.settings.Brightness = int(lc.Brightness)
			}
		}
		if lc.FanMode != c.cfg.FanModeFixed {
			c.addLog("SYS", hexSpaced(b), fmt.Sprintf("设备 data[0]=%d（锁档状态），已按固定档位回写", lc.FanMode))
		}
		c.addLog("RX", hexSpaced(b), fmt.Sprintf("当前配置 灯效=%d 亮度=%d 档位=%d", lc.LightMode, lc.Brightness, lc.Speed))
		c.mu.Unlock()
		return
	}
	c.mu.Lock()
	c.addLog("RX", hexSpaced(b), note)
	c.mu.Unlock()
}

// indexOf 返回 v 在 xs 里的下标，找不到返回 -1。
func indexOf(xs []int, v byte) int {
	for i, x := range xs {
		if byte(x) == v {
			return i
		}
	}
	return -1
}

// loop 负责轮询与周期重发。
func (c *Controller) loop(lk link) {
	poll := time.NewTicker(time.Duration(c.cfg.PollMS) * time.Millisecond)
	defer poll.Stop()

	var reapply *time.Ticker
	var reapplyC <-chan time.Time
	if c.cfg.ReapplyMS > 0 {
		reapply = time.NewTicker(time.Duration(c.cfg.ReapplyMS) * time.Millisecond)
		defer reapply.Stop()
		reapplyC = reapply.C
	}
	auto := time.NewTicker(2 * time.Second)
	defer auto.Stop()
	_ = cpuLoad() // 打一拍底，之后才有差值
	done := c.doneCh()
	for {
		select {
		case <-done:
			return
		case <-poll.C:
			_ = c.send(lk, StatusPoll(), "状态轮询 1a/06")
		case <-reapplyC:
			if f := c.currentSetFrame(); f[0] != 0 {
				_ = c.send(lk, f, "周期重发设置 06/0f")
			}
		case <-auto.C:
			c.autoStep(lk)
		}
	}
}

func (c *Controller) currentSetFrame() Frame {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastSet[0] == 0 {
		return Frame{}
	}
	return c.lastSet
}

// autoSamples 是自动模式求均值的拍数。
const autoSamples = 3

// 上行/下行阈值（回差），避免负载在边界上抖动时档位来回跳。
var (
	gearUp   = [3]int{35, 55, 80} // 0→1, 1→2, 2→3
	gearDown = [3]int{25, 45, 70} // 1→0, 2→1, 3→2
)

// stepGear 根据平滑后的负载决定下一个档位（一次只走一档）。
//
// 官方 AI 用的是 CPU 温度，我们拿不到温度（要内核驱动），用占用率近似。
func stepGear(cur, pct int) int {
	if cur < 3 && pct >= gearUp[cur] {
		return cur + 1
	}
	if cur > 0 && pct <= gearDown[cur-1] {
		return cur - 1
	}
	return cur
}

// autoStep 是「自动」模式的一拍：看负载、必要时换档并下发。
// 节奏照官方 App —— 它大约每 1–2 秒发一帧不同的档位。
func (c *Controller) autoStep(lk link) {
	c.mu.Lock()
	enabled := c.settings.FanAI
	cur := c.settings.Speed
	c.mu.Unlock()
	if !enabled {
		return
	}

	// 负载抖动大，取最近几拍的均值
	pct := cpuLoad()
	c.loadHist = append(c.loadHist, pct)
	if len(c.loadHist) > autoSamples {
		c.loadHist = c.loadHist[len(c.loadHist)-autoSamples:]
	}
	avg := 0
	for _, v := range c.loadHist {
		avg += v
	}
	avg /= len(c.loadHist)
	want := stepGear(cur, avg)

	c.mu.Lock()
	if want == cur {
		c.autoPending, c.autoCount = want, 0
		c.mu.Unlock()
		return
	}
	// 连续两拍都是同一个新档位才切，避免负载抖动时来回跳
	if want == c.autoPending {
		c.autoCount++
	} else {
		c.autoPending, c.autoCount = want, 1
	}
	if c.autoCount < 2 {
		c.mu.Unlock()
		return
	}
	c.autoPending, c.autoCount = want, 0
	c.settings.Speed = want
	f := c.buildFrame(c.settings)
	c.lastSet = f
	c.mu.Unlock()

	_ = c.send(lk, f, fmt.Sprintf("自动 负载 %d%%（%d 拍均值）→ 档位 %d", pct, len(c.loadHist), want))
}

// Apply 下发当前设置。
func (c *Controller) Apply(p Settings) error {
	c.mu.Lock()
	lk := c.lk
	c.mu.Unlock()
	if lk == nil {
		return fmt.Errorf("未连接设备")
	}

	f := c.buildFrame(p)

	c.mu.Lock()
	c.settings = p
	c.lastSet = f
	c.restored = true
	save := c.statePath != "" && !settingsEqual(p, c.lastSaved)
	if save {
		c.lastSaved = p
	}
	path := c.statePath
	c.mu.Unlock()

	if save {
		_ = SaveSettings(path, p)
	}
	return c.send(lk, f, "设置 06/0f")
}

func (c *Controller) buildFrame(p Settings) Frame {
	// 官方帧里 data[0] 恒为 0；「AI」并不是它的一个取值（实测 data[0]=1 只会
	// 把档位锁死），真正的 AI 是官方 App 自己按温度不停改档位再发帧。
	fanMode := c.cfg.FanModeFixed
	speedIdx := clamp(p.Speed, 0, len(c.cfg.Speeds)-1)
	lightIdx := clamp(p.LightMode, 0, len(c.cfg.LightModes)-1)

	r, g, b := byte(255), byte(255), byte(255)
	if len(p.Colors) > 0 {
		idx := clamp(p.ColorSel, 0, len(p.Colors)-1)
		r, g, b = parseHexColor(p.Colors[idx])
	}
	mode := byte(c.cfg.LightModes[lightIdx])
	return BuildSet(SetParams{
		LightMode:  mode,
		Brightness: byte(clamp(p.Brightness, 0, 9)),
		Speed:      byte(c.cfg.Speeds[speedIdx]),
		R:          r,
		G:          g,
		B:          b,
		MultiColor: MultiColorFor(mode),
		FanMode:    fanMode,
	}, c.cfg)
}

// SendRaw 发一帧原始十六进制（调试用）。
func (c *Controller) SendRaw(s string) error {
	c.mu.Lock()
	lk := c.lk
	c.mu.Unlock()
	if lk == nil {
		return fmt.Errorf("未连接设备")
	}
	clean := strings.NewReplacer(" ", "", "\t", "", "0x", "", ",", "", "\r", "", "\n", "").Replace(s)
	b, err := hex.DecodeString(clean)
	if err != nil {
		return fmt.Errorf("十六进制格式错误: %v", err)
	}
	if len(b) == 0 || len(b) > 20 {
		return fmt.Errorf("长度必须是 1..20 字节，当前 %d", len(b))
	}
	frame := NewFrame(0, 0, 0, nil)
	if b[0] == frameHdr && len(b) >= 3 {
		copy(frame[:], b)
	} else {
		frame = NewFrame(b[0], b[1], 0, b[2:])
	}
	return c.send(lk, frame, "手动发送")
}

func clamp(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func parseHexColor(s string) (byte, byte, byte) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) != 6 {
		return 255, 255, 255
	}
	v, err := hex.DecodeString(s)
	if err != nil || len(v) != 3 {
		return 255, 255, 255
	}
	return v[0], v[1], v[2]
}

// SetStatePath 指定设置持久化文件的位置（空字符串表示不保存）。
func (c *Controller) SetStatePath(p string) {
	c.mu.Lock()
	c.statePath = p
	c.lastSaved = c.settings
	c.mu.Unlock()
}
