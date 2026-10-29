package main

import "testing"

// 这些期望值全部来自 LEGION EDGE 自己的日志：
//   NeoUsb.log 的 "Out:"/"In:" 行、BleC.log 的 "Send data :" 行，
//   以及 2026-10-28 用官方 App 点按钮做的逐字节标定。

func TestBuildSetHengliang(t *testing.T) {
	// 恒亮 / 亮度 9 / 档位 2（野兽）；实机截到的官方帧
	cfg := defaultConfig()
	f := BuildSet(SetParams{
		LightMode:  3,
		Brightness: 9,
		Speed:      2,
		R:          0xff, G: 0xff, B: 0xff,
		MultiColor: 0,
	}, cfg)

	want := "ee 06 0f 00 00 00 03 09 02 00 00 ff ff ff 00 00 00 00 02 00"
	if got := f.Hex(); got != want {
		t.Fatalf("设置帧不符\n got: %s\nwant: %s", got, want)
	}
}

func TestBuildSetHuxi(t *testing.T) {
	// 呼吸 = 模式 2，多色标志位为 1，档位 1
	cfg := defaultConfig()
	f := BuildSet(SetParams{
		LightMode:  2,
		Brightness: 9,
		Speed:      1,
		R:          0xff, G: 0xff, B: 0xff,
		MultiColor: MultiColorFor(2),
	}, cfg)
	want := "ee 06 0f 00 00 00 02 09 02 00 00 ff ff ff 00 00 00 00 01 00"
	if got := f.Hex(); got != want {
		t.Fatalf("设置帧不符\n got: %s\nwant: %s", got, want)
	}
}

func TestMultiColorFlag(t *testing.T) {
	cases := map[byte]byte{3: 0, 2: 0, 1: 1, 0: 0}
	for mode, want := range cases {
		if got := MultiColorFor(mode); got != want {
			t.Fatalf("模式 %d 的多色标志应为 %d，得到 %d", mode, want, got)
		}
	}
}

func TestSpeedGoesToOffset13(t *testing.T) {
	cfg := defaultConfig()
	for gear := byte(0); gear < 4; gear++ {
		f := BuildSet(SetParams{LightMode: 3, Brightness: 9, Speed: gear}, cfg)
		if f[5+13] != gear {
			t.Fatalf("档位 %d 应写在 data[13]，得到 %d", gear, f[5+13])
		}
		if f[5+3] != 2 {
			t.Fatalf("data[3] 应恒为 2，得到 %d", f[5+3])
		}
	}
}

func TestStatusPollFrame(t *testing.T) {
	want := "ee 1a 06 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00"
	if got := StatusPoll().Hex(); got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestInfoQueryFrame(t *testing.T) {
	want := "ee 03 22 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00"
	if got := InfoQuery().Hex(); got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestLightReadFrame(t *testing.T) {
	want := "ee 05 0f 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00"
	if got := LightRead().Hex(); got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestParseStatusFromLog(t *testing.T) {
	cases := []struct {
		hex string
		rpm int
	}{
		{"ee1a06000001271e", 1929}, // FanRpm_Old:1929
		{"ee1a060000016619", 1625}, // FanRpm_Old:1625
		{"ee1a06000001be12", 1199}, // FanRpm_Old:1199
		{"ee1a06000001b719", 1645}, // FanRpm_Old:1645
		{"ee1a060000015315", 1364},
		{"ee1a060000015615", 1365},
		{"ee1a060000012f14", 1291},
		{"ee1a06000001e021", 2168},
		{"ee1a06000001c11f", 2032}, // 2026-10-28 实机
	}
	for _, c := range cases {
		st, ok := ParseStatus(mustHex(t, c.hex))
		if !ok {
			t.Fatalf("%s 未能解析", c.hex)
		}
		if st.RPM != c.rpm {
			t.Fatalf("%s rpm=%d want %d", c.hex, st.RPM, c.rpm)
		}
		if !st.Valid {
			t.Fatalf("%s 状态字节应为有效", c.hex)
		}
	}
}

func TestParseStatusRejectsOtherFrames(t *testing.T) {
	if _, ok := ParseStatus(mustHex(t, "ee060f0000000301020000ffffff000000000100")); ok {
		t.Fatal("设置帧不应被当成状态")
	}
	if _, ok := ParseStatus([]byte{1, 2, 3}); ok {
		t.Fatal("过短帧不应解析")
	}
}

func TestParseEcho(t *testing.T) {
	cfg := defaultConfig()
	f := BuildSet(SetParams{LightMode: 3, Brightness: 1, Speed: 2, R: 255, G: 255, B: 255}, cfg)
	if !ParseEcho(f[:], f[:]) {
		t.Fatal("同帧应判定为回显")
	}
	other := f
	other[19] = 0x99
	if ParseEcho(f[:], other[:]) {
		t.Fatal("不同帧不应判定为回显")
	}
}

func TestBrightnessOffset(t *testing.T) {
	cfg := defaultConfig()
	if cfg.BrightnessOffset != 2 {
		t.Fatalf("亮度默认应在 data[2]，得到 %d", cfg.BrightnessOffset)
	}
	f := BuildSet(SetParams{LightMode: 3, Brightness: 9, Speed: 2}, cfg)
	if f[5+2] != 9 {
		t.Fatalf("亮度应原样写 9，得到 %d", f[5+2])
	}
	cfg.BrightnessOffset = 5
	f = BuildSet(SetParams{LightMode: 3, Brightness: 7, Speed: 2}, cfg)
	if f[5+5] != 7 || f[5+2] != 0 {
		t.Fatalf("偏移改到 5 后应写 7 且 data[2] 清零，得到 %d/%d", f[5+5], f[5+2])
	}
}

func TestConfigNormalize(t *testing.T) {
	c := &Config{PollMS: 0, LightModes: []int{1}, Speeds: nil, BrightnessOffset: 99}
	c.normalize()
	if c.PollMS != 3000 || len(c.LightModes) != 4 || len(c.Speeds) != 4 || c.BrightnessOffset != 2 {
		t.Fatalf("归一化结果不对: %+v", c)
	}
	if c.LightModes[0] != 3 || c.LightModes[1] != 2 || c.LightModes[2] != 1 {
		t.Fatalf("默认灯效应为 3/2/1，得到 %v", c.LightModes)
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b := make([]byte, 0, len(s)/2)
	for i := 0; i+1 < len(s); i += 2 {
		var v byte
		for _, c := range s[i : i+2] {
			v <<= 4
			switch {
			case c >= '0' && c <= '9':
				v |= byte(c - '0')
			case c >= 'a' && c <= 'f':
				v |= byte(c-'a') + 10
			case c >= 'A' && c <= 'F':
				v |= byte(c-'A') + 10
			default:
				t.Fatalf("非法十六进制 %q", s)
			}
		}
		b = append(b, v)
	}
	return b
}
