package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config 是可以在测试机上调整的协议参数。
// 默认值来自日志实测；如果 F9 的固件版本不同，改 config.json 即可，无需重新编译。
type Config struct {
	// 蓝牙设备名（Windows 配对后显示的名字）。留空表示连接第一个可用的 F9。
	DeviceName string `json:"deviceName"`

	// 可选：手工指定特征 UUID（标准写法如 "0000fff2-0000-1000-8000-00805f9b34fb"）。
	// 留空则自动挑选"可写 + 可通知"的厂商自定义特征。
	WriteCharUUID  string `json:"writeCharUUID"`
	NotifyCharUUID string `json:"notifyCharUUID"`

	// 轮询与重发间隔（毫秒）。LEGION EDGE 大约 3 秒轮询、6 秒重发一次设置。
	PollMS    int `json:"pollMs"`
	ReapplyMS int `json:"reapplyMs"`

	// UI 索引 -> 线上字节值。默认 0,1,2,3。
	// 灯光模式：3=恒亮，2=呼吸，1=渐变，0=关（第 4 个未验证，默认给 0 表示不用）
	LightModes []int `json:"lightModes"` // 恒亮/呼吸/渐变/（预留）
	Speeds     []int `json:"speeds"`     // 安静/平衡/野兽/超能 -> data[13] 的 0..3

	FanModeAI    byte `json:"fanModeAI"`
	FanModeFixed byte `json:"fanModeFixed"`

	// 历史字段，保留只为兼容旧 config.json；设置帧里已不再使用。
	ColorCount byte `json:"colorCount"`

	// 亮度写在设置帧载荷的第几个字节（0..14）。标定值 = 2。
	BrightnessOffset int `json:"brightnessOffset"`

	// 写特征不支持"有响应写"时置 true。
	WriteWithoutResponse bool `json:"writeWithoutResponse"`
}

func defaultConfig() *Config {
	return &Config{
		// 本机实测：设备在 Windows 里叫 LEGION_F9_BT，旧版固件/其他批次叫 Viqsyx_F9_BT，
		// 用 "_F9_BT" 做子串匹配可同时覆盖两者。
		DeviceName:           "F9_BT",
		PollMS:               3000,
		ReapplyMS:            6000,
		LightModes:           []int{3, 2, 1, 0},
		Speeds:               []int{0, 1, 2, 3},
		FanModeAI:            1,
		FanModeFixed:         0,
		ColorCount:           1,
		BrightnessOffset:     2,
		WriteWithoutResponse: false,
	}
}

// LoadConfig 读取 JSON 配置；缺失字段用默认值补齐。
func LoadConfig(path string) (*Config, error) {
	cfg := defaultConfig()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, err
	}
	cfg.normalize()
	return cfg, nil
}

func (c *Config) normalize() {
	if c.PollMS <= 0 {
		c.PollMS = 3000
	}
	if c.ReapplyMS < 0 {
		c.ReapplyMS = 0
	}
	if len(c.LightModes) != 4 {
		c.LightModes = []int{3, 2, 1, 0}
	}
	if len(c.Speeds) != 4 {
		c.Speeds = []int{0, 1, 2, 3}
	}
	if c.BrightnessOffset < -1 || c.BrightnessOffset >= setDataLen {
		c.BrightnessOffset = 2
	}

}

// SaveConfig 把当前配置写回磁盘（缺少的目录会自动创建）。
func SaveConfig(path string, cfg *Config) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}
