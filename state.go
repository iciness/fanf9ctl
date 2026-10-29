package main

// 用户设置的本地持久化。
//
// 「自动」是 App 端的闭环，设备里没有这个状态；档位/灯效也会被上一版留在
// 设备的异常字节里。所以重启后要按本地记录恢复并重新下发，而不是去猜设备状态。

import (
	"encoding/json"
	"os"
)

// LoadSettings 读取上次的设置；文件不存在或损坏时返回 ok=false。
func LoadSettings(path string) (Settings, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return defaultSettings(), false
	}
	var s Settings
	if err := json.Unmarshal(raw, &s); err != nil {
		return defaultSettings(), false
	}
	if len(s.Colors) == 0 {
		s.Colors = defaultSettings().Colors
	}
	s.Brightness = clamp(s.Brightness, 0, 9)
	s.Speed = clamp(s.Speed, 0, 3)
	s.LightMode = clamp(s.LightMode, 0, 3)
	s.ColorSel = clamp(s.ColorSel, 0, len(s.Colors)-1)
	return s, true
}

// SaveSettings 把当前设置写回磁盘。
func SaveSettings(path string, s Settings) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

// settingsEqual 比较两份设置是否一致（Settings 里有切片，不能直接用 ==）。
func settingsEqual(a, b Settings) bool {
	if a.FanAI != b.FanAI || a.Speed != b.Speed || a.LightMode != b.LightMode ||
		a.Brightness != b.Brightness || a.ColorSel != b.ColorSel {
		return false
	}
	if len(a.Colors) != len(b.Colors) {
		return false
	}
	for i := range a.Colors {
		if a.Colors[i] != b.Colors[i] {
			return false
		}
	}
	return true
}
