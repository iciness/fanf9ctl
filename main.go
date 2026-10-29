package main

// 风刃 F9（Lenovo fan_F9 / Viqsyx_F9_BT）蓝牙控制器。
//
// 用法：
//
//	fanf9ctl.exe            # 打开窗口，自动连接名字含 F9_BT 的设备
//	fanf9ctl.exe -tray      # 只驻留系统托盘（开机自启用）
//	fanf9ctl.exe -sim       # 模拟模式，不碰硬件，用来看界面
//	fanf9ctl.exe -list      # 列出系统里的 BLE 设备
//	fanf9ctl.exe -diag      # 逐层打印 GATT 调用结果（排错）
//
// 协议说明见 protocol.go，逆向依据见 README.md。

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

func main() {
	var (
		cfgPath  = flag.String("config", "", "配置文件路径（默认 exe 同目录 config.json）")
		sim      = flag.Bool("sim", false, "模拟模式：不连接硬件，只看界面")
		device   = flag.String("device", "", "覆盖设备名/地址")
		trayOnly = flag.Bool("tray", false, "启动后只驻留系统托盘，不显示窗口（开机自启用）")
		list     = flag.Bool("list", false, "只列出系统中的 BLE 设备后退出")
		diag     = flag.Bool("diag", false, "枚举 GATT 服务/特征并尝试订阅后退出（排错用）")
		debug    = flag.Bool("debug", false, "显示日志控制台窗口")
	)
	flag.Parse()

	// 正常启动是纯 GUI（-H windowsgui），不出现黑框；
	// -debug 单独开一个日志窗口，命令行模式则复用/附加控制台
	if *debug {
		showConsole()
		uiLog("调试控制台: %v", hasConsole())
	}
	if *list || *diag {
		ensureConsole()
	}

	if *diag {
		if err := RunDiag(*device); err != nil {
			fmt.Println("诊断失败:", err)
			os.Exit(1)
		}
		return
	}

	if *list {
		devs, err := ListDevices()
		if err != nil {
			fmt.Println("枚举失败:", err)
			os.Exit(1)
		}
		for _, d := range devs {
			fmt.Printf("%-17s  %-20s  %s\n", d.Address, d.Name, d.Path)
		}
		if len(devs) == 0 {
			fmt.Println("（没有已配对的 BLE 设备）")
		}
		return
	}

	cfgFile := *cfgPath
	if cfgFile == "" {
		cfgFile = filepath.Join(exeDir(), "config.json")
	}
	cfg, err := LoadConfig(cfgFile)
	if err != nil {
		log.Printf("读取配置失败，使用默认值: %v", err)
		cfg = defaultConfig()
	}
	// 首次运行写出模板，方便改
	if _, err := os.Stat(cfgFile); os.IsNotExist(err) {
		if err := SaveConfig(cfgFile, cfg); err != nil {
			log.Printf("写出配置模板失败: %v", err)
		} else {
			log.Printf("已生成配置模板: %s", cfgFile)
		}
	}

	startCPUMonitor()

	stateFile := filepath.Join(exeDir(), "state.json")
	initial, restored := LoadSettings(stateFile)
	if restored {
		log.Printf("恢复上次设置: 自动=%v 档位=%d 灯效=%d 亮度=%d",
			initial.FanAI, initial.Speed, initial.LightMode, initial.Brightness)
	}
	ctl := NewController(cfg, *sim, initial, restored)
	ctl.SetStatePath(stateFile)

	// 自动连接（失败不退出，界面上可手动重试）
	go func() {
		time.Sleep(600 * time.Millisecond)
		if err := ctl.Connect(*device); err != nil {
			log.Printf("自动连接失败: %v", err)
		}
	}()

	// 编译成 GUI 子系统，双击不会有黑框；-debug 时上面已经挂好控制台
	runGUI(ctl, *device, *trayOnly)
	os.Exit(0)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func exeDir() string {
	p, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(p)
}
