# 逆向笔记（供后续在带硬件的机器上继续）

## 1. 目标

| 文件 | 大小 | SHA-256 |
|---|---|---|
| `C:\Program Files (x86)\LEGION EDGE\LEGION EDGE.exe` | 5,712,896 | `4cd0f791c01172d06210b9012c60ba38f164e32dd1fc931900dd3e4f3b6edd8d` |
| `C:\Program Files (x86)\LEGION EDGE\evsBleCommunit.dll` | 159,232 | `e60af273e314b763efc119c09e136c74a8e975cf6f39c4851a4e3430aaf814d9` |
| `...\DefaultData\DefaultData.json` | — | `62bfd571be89b7f4c9dee38ea1a9b4fd6554c7393d81be1dfbe8d872096d4fa5` |

- 两者都是 PE32 i386，原生 C++/ATL（MSVCP140），UI 是 DuiLib。
- EXE 的 PDB 是 `NeoUsb.pdb`（GUID `9c63a970-db02-45ef-88f2-c9470caf7cb6`），安装目录里还有
  `1_Renametoexe.bat` 把 `NeoUsb.exe` 改名为 `LEGION EDGE.exe`——说明这是 EVS/亿联方案的通用工具改名分发。
- DLL 的 PDB：`D:\recv\...\Edge_Code_1024\evsnWinBle\src\Release\evsBleCommunit.pdb`。

## 2. 分层

```
LEGION EDGE.exe          协议层：拼帧、校验、解析、业务/UI      （PDB: NeoUsb.pdb）
  └─ evsBleCommunit.dll  BLE 传输层：Win32 GATT，仅收发 20 字节
       └─ bluetoothapis.dll  BluetoothGATTGetServices / GetCharacteristics /
                             RegisterEvent / SetCharacteristicValue / SetDescriptorValue
```

EXE 从 DLL 导入的只有 6 个函数：

```
GetBleFan, InitBle, UnInitBle, CloseBleFanDevice, HelpBleReadDevice, HelpBleWriteOverlap
```

`HelpBleWriteOverlap` 的 IAT 槽位：RVA `0x3B64F0` → VA `0x7B64F0`。
搜索 `FF 15 F0 64 7B 00`（`call [0x7B64F0]`）只有 **1 处**：`0x5905A2`，位于 `FUN_00590240`。

## 3. 关键函数（Ghidra 12.1.4，image base 0x400000）

| 地址 | 作用 |
|---|---|
| `FUN_00590240` | **传输层核心**：入参 30 字节 HID 包，改写成 20 字节 BLE 帧（`[0]=0xEE`，去掉 usb[1..2] 与 usb[7]），调用 `HelpBleWriteOverlap`；同时打印 `Out:` / `Ble model write command %02x` / `Ble model read command %02x` / `In:` |
| `FUN_00594840` | **读命令封装**：`local_48[0]=4`、`[3]=0x1A`、`[4]=sub`、`[5..6]=arg16`，`[1..2]=sum([3..63])`，再调 `FUN_00590240` |
| `FUN_005ba480` | **状态解析**：`FUN_00594840(buf,6,0)` 取回 6 字节；`rpm=(u32&0xFFFF)>>2`、`light=(u32>>16)&0xFF`；日志 `Get Fan Win Light %d`、`SetFanLightSelect %d` |
| `FUN_005babb0` | 转速显示：`((rpm+50)/100)*100`，写控件 `rpm_text`，日志 `FanRpm_Old:%d   FanRpm_New:%d` |
| `FUN_0050bce0` | 灯光配置写入（`SetLightInfo%d`），字段顺序 `SelectItem → Light → Speed → MultiColor → Red → Green → Blue → …` |
| `FUN_00407b60` | 静态设备表初始化（PID/VID/型号名，含 `Fan_F9`、`Mouse_M3_White` 等，先 memset 再逐字段赋值） |

关键字符串地址（可用来定位同类逻辑）：

| 地址 | 字符串 |
|---|---|
| `0x8121a4` | `SetLightInfo%d` |
| `0x7fffbc` | `SetFanLightBrightness %d` |
| `0x802214` | `Fan Win Light %d` |
| `0x837204` | `FanRpm_Old:%d   FanRpm_New:%d` |
| `0x7fd464` 等 | `Fan_F9` |
| `0x8227f4` / `0x82a3bc` | `FAN_F9` / `FAN_F9_UPDATA_FW` |

## 4. DLL 侧

- 设备枚举：SetupDi + 设备接口类 `{781aee18-7733-4ce4-add0-91f41c67b592}`（`GUID_BLUETOOTHLE_DEVICE_INTERFACE`），
  路径形如 `\\?\bthle#dev_<地址>#<实例>#{类GUID}`。
- 日志里出现过的字符串：`Send data :%s`、`Received data :%s`、`Send data tm:%d ms`、
  `BluetoothGATTSetCharacteristicValue ble send time:%d ms`、`BTHLE\DEV_`、
  `Unable to subscribe to the characteristic`、`characteristic is not notifiable or indicatable`。
- 日志落盘：`%LOCALAPPDATA%\LEGION EDGE\BleC.log`。

> 注意：DLL 里那个 `{4695579F-999A-41d4-9FAF-EFFCE93EBF15}` **不是** GATT UUID，
> 它是 `CUpdateOta` 构造函数里 `RegisterWindowMessageW` 的消息名，别拿它当服务 UUID。

## 5. 设备标识（实测）

```
Windows 配对名: LEGION_F9_BT      （旧批次/旧固件可能是 Viqsyx_F9_BT）
地址（本机）:    d1:03:d1:xx:xx:xx  /  d1:03:d1:xx:xx:xx
DefaultData.json: PID 320F, VID 5090, Chip VS11M17A,
                  DeviceNUMInfo = eevision_A_ms_001_20180108_ak60
静态设备表里另有 0x17EF(Lenovo)/0xF00C 组合
```

## 6. 工具链（想继续挖时）

- Windows SDK 头文件是权威结构定义来源：
  `C:\Program Files (x86)\Windows Kits\10\Include\<ver>\um\bthledef.h`
  （`BTH_LE_GATT_CHARACTERISTIC` 36 字节、`BTH_LE_UUID` 20 字节、`BTH_LE_GATT_DESCRIPTOR_VALUE` 80 字节）。
- Ghidra 12.1.4 + REA MCP 可直接对 `LEGION EDGE.exe` 做 `analyze_function` / `xrefs`。
- 复现协议最快的路径不是继续读代码，而是：**打开官方 App 点一次某个按钮，然后看 BleC.log 新增了哪一帧**。

## 7. 待办（按优先级）

1. **亮度字节**：在官方 App 里把亮度从 9 拖到 1，看 `BleC.log` 是否多出一种帧。若没有，说明亮度只在 USB 通道下发。
2. **AI 模式**：切到 AI 模式，看是否有额外帧携带 CPU 温度。
3. **渐变/多色**：加两个颜色，看 `data[9..12]` 是否被填。
4. **各索引语义**：逐个点 4 个风扇档 + 4 个灯效，记录 `data[1]`/`data[3]` 的实际取值，回填 `config.json`。
