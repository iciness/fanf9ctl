//go:build windows

package main

// 原生 Win32 界面（GDI 绘制，无 cgo、无第三方依赖）。
//
// 之前是用 Edge/Chrome 的 --app 模式开一个本地页面当界面，但浏览器
// 在很多机器上（配置目录被占用、窗口坐标排在已拔掉的外接屏上、
// 企业策略、受限令牌……）不一定能开出来，失败时只有一个黑框。
// 这里改成直接建一个窗口，所有控件自己画、自己处理鼠标。

import (
	"fmt"
	"log"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// ---------- Win32 常量 ----------

const (
	wsOverlappedWindow = 0x00CF0000
	swShow             = 5
	cwUseDefault       = 0x80000000
	csHRedraw          = 0x0002
	csVRedraw          = 0x0001

	wmDestroy     = 0x0002
	wmSize        = 0x0005
	wmPaint       = 0x000F
	wmClose       = 0x0010
	wmEraseBkgnd  = 0x0014
	wmSetCursor   = 0x0020
	wmTimer       = 0x0113
	wmLButtonDown = 0x0201
	wmLButtonUp   = 0x0202
	wmMouseMove   = 0x0200

	dtCenter      = 0x0001
	dtRight       = 0x0002
	dtVCenter     = 0x0004
	dtSingleLine  = 0x0020
	dtNoPrefix    = 0x0800
	dtEndEllipsis = 0x8000

	idcArrow    = 32512
	srcCopy     = 0x00CC0020
	transparent = 1

	swpNoZOrder = 0x0004
	swpNoMove   = 0x0002

	// 托盘
	trayMsg         = 0x8000 + 1 // WM_APP + 1
	nimAdd          = 0
	nimDelete       = 2
	nimSetVersion   = 4
	nifMessage      = 0x00000001
	nifIcon         = 0x00000002
	nifTip          = 0x00000004
	idTray          = 1
	idMenuOpen      = 1001
	idMenuExit      = 1002
	idMenuAuto      = 1003
	mfSeparator     = 0x0800
	mfChecked       = 0x0008
	mfString        = 0x00000000
	tpmRightBtn     = 0x0002
	tpmRetCmd       = 0x0100
	wmLButtonDblClk = 0x0203
	wmLButtonUpMsg  = 0x0202
	wmRButtonUp     = 0x0205
	wmSetIcon       = 0x0080
	idiApplication  = 32512
)

// 灯光模式的 UI 下标：0=恒亮 1=呼吸 2=渐变（对应 config.lightModes 的 3/2/1）
const (
	lightSteady   = 0
	lightGradient = 2
)

// 配色（COLORREF 是 0x00BBGGRR，和 CSS 的 #RRGGBB 反着来）
const (
	colBg     uint32 = 0x001c1614 // #14161c
	colPanel  uint32 = 0x00100c0a // #0a0c10
	colCard   uint32 = 0x0028201e // #1e2028
	colCardHi uint32 = 0x002f2623 // #23262f
	colLine   uint32 = 0x00332a26 // #262a33
	colText   uint32 = 0x00f3ece9 // #e9ecf3
	colMuted  uint32 = 0x00a2918b // #8b91a2
	colAccent uint32 = 0x00ed6f2f // #2f6fed
	colDanger uint32 = 0x005d5dff // #ff5d5d
	colOk     uint32 = 0x009fd338 // #38d39f
	colWhite  uint32 = 0x00ffffff
)

// ---------- Win32 结构 ----------

type winRect struct{ Left, Top, Right, Bottom int32 }

type winPoint struct{ X, Y int32 }

type winMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      winPoint
}

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type paintStruct struct {
	Hdc         uintptr
	FErase      int32
	RcPaint     winRect
	FRestore    int32
	FIncUpdate  int32
	RgbReserved [32]byte
}

// ---------- DLL ----------

var (
	modUser32Gui = syscall.NewLazyDLL("user32.dll")
	modGdi32     = syscall.NewLazyDLL("gdi32.dll")
	modKernelGui = syscall.NewLazyDLL("kernel32.dll")

	pRegisterClassExW    = modUser32Gui.NewProc("RegisterClassExW")
	pCreateWindowExW     = modUser32Gui.NewProc("CreateWindowExW")
	pDefWindowProcW      = modUser32Gui.NewProc("DefWindowProcW")
	pShowWindow          = modUser32Gui.NewProc("ShowWindow")
	pSetForegroundWindow = modUser32Gui.NewProc("SetForegroundWindow")
	pUpdateWindow        = modUser32Gui.NewProc("UpdateWindow")
	pGetMessageW         = modUser32Gui.NewProc("GetMessageW")
	pTranslateMessage    = modUser32Gui.NewProc("TranslateMessage")
	pDispatchMessageW    = modUser32Gui.NewProc("DispatchMessageW")
	pPostQuitMessage     = modUser32Gui.NewProc("PostQuitMessage")
	pDestroyWindow       = modUser32Gui.NewProc("DestroyWindow")
	pBeginPaint          = modUser32Gui.NewProc("BeginPaint")
	pEndPaint            = modUser32Gui.NewProc("EndPaint")
	pGetClientRect       = modUser32Gui.NewProc("GetClientRect")
	pInvalidateRect      = modUser32Gui.NewProc("InvalidateRect")
	pLoadCursorW         = modUser32Gui.NewProc("LoadCursorW")
	pSetCursor           = modUser32Gui.NewProc("SetCursor")
	pSetTimer            = modUser32Gui.NewProc("SetTimer")
	pDrawTextW           = modUser32Gui.NewProc("DrawTextW")
	pFillRect            = modUser32Gui.NewProc("FillRect")
	pSetProcessDPIAware  = modUser32Gui.NewProc("SetProcessDPIAware")
	pGetDpiForSystem     = modUser32Gui.NewProc("GetDpiForSystem")
	pGetDpiForWindow     = modUser32Gui.NewProc("GetDpiForWindow")
	pSetWindowPos        = modUser32Gui.NewProc("SetWindowPos")
	pGetSystemMetrics    = modUser32Gui.NewProc("GetSystemMetrics")
	pIsWindowVisible     = modUser32Gui.NewProc("IsWindowVisible")
	pLoadIconW           = modUser32Gui.NewProc("LoadIconW")
	pGetCursorPos        = modUser32Gui.NewProc("GetCursorPos")
	pCreatePopupMenu     = modUser32Gui.NewProc("CreatePopupMenu")
	pAppendMenuW         = modUser32Gui.NewProc("AppendMenuW")
	pTrackPopupMenu      = modUser32Gui.NewProc("TrackPopupMenu")
	pDestroyMenu         = modUser32Gui.NewProc("DestroyMenu")
	pPostMessage         = modUser32Gui.NewProc("PostMessageW")
	pSendMessageW        = modUser32Gui.NewProc("SendMessageW")

	pShellNotifyIconW = syscall.NewLazyDLL("shell32.dll").NewProc("Shell_NotifyIconW")
	pGetTokenInfo     = syscall.NewLazyDLL("advapi32.dll").NewProc("GetTokenInformation")
	pGetSidSubCount   = syscall.NewLazyDLL("advapi32.dll").NewProc("GetSidSubAuthorityCount")
	pGetSidSubAuth    = syscall.NewLazyDLL("advapi32.dll").NewProc("GetSidSubAuthority")

	pCreateCompatibleDC     = modGdi32.NewProc("CreateCompatibleDC")
	pCreateCompatibleBitmap = modGdi32.NewProc("CreateCompatibleBitmap")
	pSelectObject           = modGdi32.NewProc("SelectObject")
	pDeleteObject           = modGdi32.NewProc("DeleteObject")
	pDeleteDC               = modGdi32.NewProc("DeleteDC")
	pBitBlt                 = modGdi32.NewProc("BitBlt")
	pCreateSolidBrush       = modGdi32.NewProc("CreateSolidBrush")
	pCreateFontW            = modGdi32.NewProc("CreateFontW")
	pSetTextColor           = modGdi32.NewProc("SetTextColor")
	pSetBkMode              = modGdi32.NewProc("SetBkMode")

	pGetModuleHandleW      = modKernelGui.NewProc("GetModuleHandleW")
	pGetConsoleWindow      = modKernelGui.NewProc("GetConsoleWindow")
	pGetConsoleProcessList = modKernelGui.NewProc("GetConsoleProcessList")
)

// ---------- 状态 ----------

type hotRegion struct {
	r  winRect
	id string
}

type guiApp struct {
	ctl        *Controller
	device     string
	hwnd       uintptr
	hinst      uintptr
	cursor     uintptr
	wndProcPtr uintptr
	scale      float64

	memDC  uintptr
	memBmp uintptr
	bufW   int32
	bufH   int32

	fTitle uintptr
	fBody  uintptr
	fSmall uintptr
	fBig   uintptr
	fMono  uintptr

	hits     []hotRegion
	hoverID  string
	dragging bool
	bright   int // 0 = 用设备里的值，1..9 = 拖动中的预览
	cpu      int // CPU 占用率（0..100）

	trayAdded  bool
	trayIcon   uintptr
	hIconBig   uintptr
	hIconSmall uintptr
	logged     bool
}

// runGUI 建窗口并跑消息循环，直到窗口关闭。
func runGUI(ctl *Controller, device string, startHidden bool) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	pSetProcessDPIAware.Call()

	g := &guiApp{ctl: ctl, device: device, scale: 1}
	g.hinst, _, _ = pGetModuleHandleW.Call(0)
	g.cursor, _, _ = pLoadCursorW.Call(0, idcArrow)
	g.wndProcPtr = syscall.NewCallback(g.wndProc)

	className, _ := syscall.UTF16PtrFromString("FanF9CtlNativeUI")
	g.hIconBig = loadIcon(32)
	g.hIconSmall = loadIcon(16)
	wc := wndClassEx{
		Style:         csHRedraw | csVRedraw,
		LpfnWndProc:   g.wndProcPtr,
		HInstance:     g.hinst,
		HIcon:         g.hIconBig,
		HCursor:       g.cursor,
		HbrBackground: 0,
		LpszClassName: className,
		HIconSm:       g.hIconSmall,
	}
	wc.CbSize = uint32(unsafe.Sizeof(wc))
	if r, _, err := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		showFatal("注册窗口类失败: " + err.Error())
		return
	}

	// 先按 96 DPI 建窗，拿到窗口所在显示器的真实 DPI 后再放大并重排。
	title, _ := syscall.UTF16PtrFromString("风刃F9 控制台")
	g.hwnd, _, _ = pCreateWindowExW.Call(0,
		uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(title)),
		wsOverlappedWindow, cwUseDefault, cwUseDefault,
		uintptr(baseW), uintptr(baseH), 0, 0, g.hinst, 0)
	if g.hwnd == 0 {
		showFatal("创建窗口失败")
		return
	}

	g.scale = g.windowScale()
	if g.scale != 1 {
		pSetWindowPos.Call(g.hwnd, 0, 0, 0, uintptr(g.s(baseW)), uintptr(g.s(baseH)),
			swpNoMove|swpNoZOrder)
	}

	// 显式设置窗口图标：只配窗口类时，标题栏/任务栏不一定采用
	pSendMessageW.Call(g.hwnd, wmSetIcon, 1, g.hIconBig)   // ICON_BIG
	pSendMessageW.Call(g.hwnd, wmSetIcon, 0, g.hIconSmall) // ICON_SMALL

	g.createFonts()
	if !startHidden {
		pShowWindow.Call(g.hwnd, swShow)
		pSetForegroundWindow.Call(g.hwnd)
	}
	pUpdateWindow.Call(g.hwnd)
	pSetTimer.Call(g.hwnd, 1, 250, 0)

	// 托盘放在窗口显示之后再挂：万一失败弹框，也不会把主窗口挡住
	ok := g.addTray()

	if !ok && startHidden {
		// 开机自启模式：托盘不可用就改为显示窗口，别让程序"消失"（也不弹框，免得开机就弹）
		pShowWindow.Call(g.hwnd, swShow)
		pSetForegroundWindow.Call(g.hwnd)
		uiLog("托盘不可用，已改为显示窗口")
	}
	if !ok && !startHidden {
		msg := fmt.Sprintf("无法添加到系统托盘（进程完整性 %s）。\n\n程序会保持窗口模式，点 × 将直接退出。\n如果是从下载目录/沙箱目录运行的，把它复制到普通文件夹再试。", tokenIntegrity())
		showFatal(msg)
	}

	var msg winMsg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

// 窗口基础尺寸（96 DPI 下的逻辑像素）。
const (
	baseW int32 = 980
	baseH int32 = 720
)

func (g *guiApp) windowScale() float64 {
	d := int32(96)
	if r, _, _ := pGetDpiForWindow.Call(g.hwnd); r >= 96 {
		d = int32(r)
	} else if r, _, _ := pGetDpiForSystem.Call(); r >= 96 {
		d = int32(r)
	}
	s := float64(d) / 96.0

	// 别超出屏幕工作区
	sw, _, _ := pGetSystemMetrics.Call(0)
	sh, _, _ := pGetSystemMetrics.Call(1)
	if sw > 0 && sh > 0 {
		maxW := float64(sw) * 0.95
		maxH := float64(sh) * 0.92
		if f := maxW / (float64(baseW) * s); f < 1 {
			s *= f
		}
		if f := maxH / (float64(baseH) * s); f < 1 {
			s *= f
		}
	}
	if s < 0.75 {
		s = 0.75
	}
	return s
}

func showFatal(msg string) {
	log.Printf("%s", msg)
	u, _ := syscall.UTF16PtrFromString(msg)
	t, _ := syscall.UTF16PtrFromString("风刃F9 控制台")
	pMessageBoxW.Call(0, uintptr(unsafe.Pointer(u)), uintptr(unsafe.Pointer(t)), 0x10)
}

var pMessageBoxW = modUser32Gui.NewProc("MessageBoxW")

// ---------- 尺寸与字体 ----------

func (g *guiApp) s(v int32) int32 {
	return int32(float64(v) * g.scale)
}

func (g *guiApp) mkFont(size int32, weight int, face string) uintptr {
	name, _ := syscall.UTF16PtrFromString(face)
	h, _, _ := pCreateFontW.Call(
		uintptr(-g.s(size)), 0, 0, 0, uintptr(weight), 0, 0, 0,
		1, 0, 0, 5, 0, uintptr(unsafe.Pointer(name)))
	return h
}

func (g *guiApp) createFonts() {
	g.fTitle = g.mkFont(19, 600, "Microsoft YaHei UI")
	g.fBody = g.mkFont(14, 400, "Microsoft YaHei UI")
	g.fSmall = g.mkFont(12, 400, "Microsoft YaHei UI")
	g.fBig = g.mkFont(52, 700, "Microsoft YaHei UI")
	g.fMono = g.mkFont(12, 400, "Consolas")
}

// ---------- 绘制原语 ----------

func (g *guiApp) fill(hdc uintptr, r winRect, color uint32) {
	br, _, _ := pCreateSolidBrush.Call(uintptr(color))
	rc := r
	pFillRect.Call(hdc, uintptr(unsafe.Pointer(&rc)), br)
	pDeleteObject.Call(br)
}

func (g *guiApp) text(hdc uintptr, s string, r winRect, font uintptr, color uint32, flags uint32) {
	if s == "" || r.Right <= r.Left {
		return
	}
	old, _, _ := pSelectObject.Call(hdc, font)
	pSetTextColor.Call(hdc, uintptr(color))
	pSetBkMode.Call(hdc, transparent)
	u := syscall.StringToUTF16(s)
	u = u[:len(u)-1] // 去掉结尾 0，改用显式长度
	rc := r
	pDrawTextW.Call(hdc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)),
		uintptr(unsafe.Pointer(&rc)), uintptr(flags|dtNoPrefix))
	pSelectObject.Call(hdc, old)
}

// ---------- 窗口过程 ----------

func (g *guiApp) wndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	switch msg {
	case wmEraseBkgnd:
		return 1
	case wmPaint:
		g.onPaint(hwnd)
		return 0
	case wmSize:
		g.dropBuffer()
	case wmTimer:
		if v := cpuLoad(); v > 0 || g.cpu == 0 {
			g.cpu = v
		}
		pInvalidateRect.Call(hwnd, 0, 0)
		return 0
	case wmLButtonDown:
		g.onDown(int32(lparam&0xffff), int32((lparam>>16)&0xffff))
		return 0
	case wmMouseMove:
		g.onMove(int32(lparam&0xffff), int32((lparam>>16)&0xffff))
		return 0
	case wmLButtonUp:
		g.onUp()
		return 0
	case wmSetCursor:
		pSetCursor.Call(g.cursor)
		return 1
	case trayMsg:
		// v0 约定：lParam 直接是鼠标消息；v4 约定：HIWORD(wParam) 是通知码。
		// 两种都认，免得版本设置一变右键就失效。
		event := uint32(lparam)
		if hi := uint32(wparam >> 16); hi != 0 {
			event = hi
		}
		switch event {
		case wmLButtonUpMsg, wmLButtonDblClk:
			g.showFromTray()
		case wmRButtonUp:
			g.showMenu()
		}
		return 0
	case wmClose:
		// 关窗口 = 缩到托盘；托盘没加成功就直接退出，免得程序"消失"
		if g.trayAdded {
			g.hideToTray()
		} else {
			g.removeTray()
			pDestroyWindow.Call(hwnd)
		}
		return 0
	case wmDestroy:
		g.removeTray()
		pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
}

func (g *guiApp) dropBuffer() {
	if g.memBmp != 0 {
		pDeleteObject.Call(g.memBmp)
		g.memBmp = 0
	}
	if g.memDC != 0 {
		pDeleteDC.Call(g.memDC)
		g.memDC = 0
	}
	g.bufW, g.bufH = 0, 0
}

func (g *guiApp) ensureBuffer(hdc uintptr, w, h int32) {
	if g.memDC != 0 && g.bufW == w && g.bufH == h {
		return
	}
	g.dropBuffer()
	g.memDC, _, _ = pCreateCompatibleDC.Call(hdc)
	g.memBmp, _, _ = pCreateCompatibleBitmap.Call(hdc, uintptr(w), uintptr(h))
	pSelectObject.Call(g.memDC, g.memBmp)
	g.bufW, g.bufH = w, h
}

func (g *guiApp) onPaint(hwnd uintptr) {
	var ps paintStruct
	hdc, _, _ := pBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	var rc winRect
	pGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
	if rc.Right <= 0 || rc.Bottom <= 0 {
		pEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		return
	}
	g.ensureBuffer(hdc, rc.Right, rc.Bottom)
	g.paintAll(g.memDC, rc.Right, rc.Bottom)
	pBitBlt.Call(hdc, 0, 0, uintptr(rc.Right), uintptr(rc.Bottom),
		g.memDC, 0, 0, srcCopy)
	pEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
}

// ---------- 布局与绘制 ----------

func (g *guiApp) button(hdc uintptr, id, label string, r winRect, active bool) {
	bg := colCard
	fg := colText
	switch {
	case active:
		bg, fg = colAccent, colWhite
	case g.hoverID == id:
		bg = colCardHi
	}
	g.fill(hdc, r, bg)
	g.text(hdc, label, r, g.fBody, fg, dtCenter|dtVCenter|dtSingleLine)
	g.hits = append(g.hits, hotRegion{r, id})
}

func (g *guiApp) card(hdc uintptr, r winRect) {
	g.fill(hdc, r, colCard)
}

func (g *guiApp) paintAll(hdc uintptr, w, h int32) {
	snap := g.ctl.Snapshot()
	g.hits = g.hits[:0]

	g.fill(hdc, winRect{0, 0, w, h}, colBg)

	padX := int32(float64(w) * 0.026)
	padY := int32(float64(h) * 0.034)
	headerH := int32(float64(h) * 0.105)

	// 标题
	g.text(hdc, "风刃F9 控制台", winRect{padX, 0, w * 45 / 100, headerH},
		g.fTitle, colText, dtVCenter|dtSingleLine)

	// 状态 + 连接按钮
	status, sc := "未连接", colMuted
	if snap.Connected {
		status, sc = "已连接", colOk
	}
	if snap.Simulated {
		status, sc = "模拟模式", colAccent
	}
	if snap.LastError != "" {
		status, sc = "连接失败", colDanger
	}
	connLabel := "连 接"
	if snap.Connected {
		connLabel = "断 开"
	}
	connW := int32(float64(w) * 0.11)
	connR := winRect{w - padX - connW, headerH / 4, w - padX, headerH * 3 / 4}
	g.button(hdc, "conn", connLabel, connR, snap.Connected)
	g.text(hdc, "● "+status, winRect{w - padX - connW - int32(float64(w)*0.26), 0, w - padX - connW - padX, headerH},
		g.fBody, sc, dtVCenter|dtSingleLine|dtRight)

	contentTop := headerH + padY
	contentH := h - contentTop - padY
	if contentH < g.s(240) {
		contentH = g.s(240)
	}

	// 右列：四张卡片 + 底部日志
	rowH := int32(float64(contentH) * 0.155)
	gapY := int32(float64(contentH) * 0.018)
	logTop := contentTop + rowH*4 + gapY*4 + gapY
	leftW := int32(float64(w) * 0.27)
	leftCard := winRect{padX, contentTop, padX + leftW, logTop - gapY}
	g.card(hdc, leftCard)

	g.text(hdc, "风扇转速", winRect{leftCard.Left + g.s(18), leftCard.Top + g.s(14), leftCard.Right, leftCard.Top + g.s(40)},
		g.fSmall, colMuted, dtSingleLine)

	rpm := "--"
	if snap.Connected && snap.RPM > 0 {
		rpm = fmt.Sprintf("%d", snap.RPM)
	}
	rpmRect := winRect{leftCard.Left + g.s(16), leftCard.Top + int32(float64(leftCard.Bottom-leftCard.Top)*0.22),
		leftCard.Right - g.s(8), leftCard.Top + int32(float64(leftCard.Bottom-leftCard.Top)*0.55)}
	g.text(hdc, rpm, rpmRect, g.fBig, colText, dtVCenter|dtSingleLine)
	g.text(hdc, "RPM", winRect{rpmRect.Left + g.s(2), rpmRect.Bottom - g.s(4), rpmRect.Right, rpmRect.Bottom + g.s(20)},
		g.fSmall, colMuted, dtSingleLine)

	addr := "未连接设备"
	if snap.Connected {
		addr = snap.Link.Address
	}
	g.text(hdc, addr, winRect{leftCard.Left + g.s(18), leftCard.Bottom - g.s(44), leftCard.Right - g.s(12), leftCard.Bottom - g.s(14)},
		g.fMono, colMuted, dtSingleLine|dtEndEllipsis)
	if snap.LastError != "" {
		g.text(hdc, snap.LastError, winRect{leftCard.Left + g.s(18), leftCard.Bottom - g.s(78), leftCard.Right - g.s(12), leftCard.Bottom - g.s(48)},
			g.fSmall, colDanger, dtSingleLine|dtEndEllipsis)
	}

	rx := padX + leftW + padX
	rw := w - padX - rx
	if rw < g.s(200) {
		rw = g.s(200)
	}

	// 散热模式
	c1 := winRect{rx, contentTop, rx + rw, contentTop + rowH}
	g.card(hdc, c1)
	g.text(hdc, "散热模式", winRect{c1.Left + g.s(16), c1.Top + g.s(8), c1.Left + g.s(140), c1.Top + g.s(32)},
		g.fSmall, colMuted, dtSingleLine)
	{
		speeds := []string{"安静", "平衡", "野兽", "超能"}
		info := fmt.Sprintf("CPU %d%%", g.cpu)
		if snap.Settings.FanAI {
			idx := snap.Settings.Speed
			if idx < 0 || idx >= len(speeds) {
				idx = 0
			}
			info = fmt.Sprintf("自动 · %s · CPU %d%%", speeds[idx], g.cpu)
		}
		g.text(hdc, info, winRect{c1.Left + g.s(140), c1.Top + g.s(8), c1.Right - g.s(16), c1.Top + g.s(32)},
			g.fSmall, colMuted, dtRight|dtSingleLine)
	}
	gap := g.s(8)
	bw := (rw - g.s(32) - gap*4) / 5
	by := c1.Top + int32(float64(rowH)*0.42)
	bh := int32(float64(rowH) * 0.36)
	bx := c1.Left + g.s(16)
	for i, name := range []string{"安静", "平衡", "野兽", "超能"} {
		g.button(hdc, fmt.Sprintf("speed%d", i), name,
			winRect{bx, by, bx + bw, by + bh}, !snap.Settings.FanAI && snap.Settings.Speed == i)
		bx += bw + gap
	}
	g.button(hdc, "ai", "自动", winRect{bx, by, bx + bw, by + bh}, snap.Settings.FanAI)

	// 灯光模式
	y2 := c1.Bottom + gapY
	c2 := winRect{rx, y2, rx + rw, y2 + rowH}
	g.card(hdc, c2)
	g.text(hdc, "灯光模式", winRect{c2.Left + g.s(16), c2.Top + g.s(8), c2.Right, c2.Top + g.s(32)},
		g.fSmall, colMuted, dtSingleLine)
	lbw := (rw - g.s(32) - gap*2) / 3
	lbx := c2.Left + g.s(16)
	lby := c2.Top + int32(float64(rowH)*0.42)
	for i, name := range []string{"恒亮", "呼吸", "渐变"} {
		g.button(hdc, fmt.Sprintf("light%d", i), name,
			winRect{lbx, lby, lbx + lbw, lby + bh}, snap.Settings.LightMode == i)
		lbx += lbw + gap
	}

	// 亮度
	y3 := c2.Bottom + gapY
	c3 := winRect{rx, y3, rx + rw, y3 + rowH}
	g.card(hdc, c3)
	g.text(hdc, "亮度", winRect{c3.Left + g.s(16), c3.Top + g.s(8), c3.Right, c3.Top + g.s(32)},
		g.fSmall, colMuted, dtSingleLine)
	bright := snap.Settings.Brightness
	if g.bright > 0 {
		bright = g.bright
	}
	trackL := c3.Left + g.s(16)
	trackR := c3.Right - g.s(70)
	trackY := c3.Top + int32(float64(rowH)*0.62)
	g.fill(hdc, winRect{trackL, trackY, trackR, trackY + g.s(6)}, colLine)
	frac := float64(bright-1) / 8
	knobX := trackL + int32(frac*float64(trackR-trackL))
	g.fill(hdc, winRect{knobX - g.s(3), trackY - g.s(7), knobX + g.s(3), trackY + g.s(13)}, colAccent)
	g.text(hdc, fmt.Sprintf("%d", bright), winRect{trackR + g.s(6), trackY - g.s(16), c3.Right - g.s(12), trackY + g.s(20)},
		g.fBody, colText, dtCenter|dtSingleLine)
	g.hits = append(g.hits, hotRegion{winRect{trackL, trackY - g.s(16), trackR, trackY + g.s(18)}, "bright"})

	// 颜色
	y4 := c3.Bottom + gapY
	c4 := winRect{rx, y4, rx + rw, y4 + rowH}
	g.card(hdc, c4)
	g.text(hdc, "颜色", winRect{c4.Left + g.s(16), c4.Top + g.s(8), c4.Right, c4.Top + g.s(32)},
		g.fSmall, colMuted, dtSingleLine)
	// 渐变模式下设备自己轮换颜色，选色没有意义，直接显示说明
	palette := []uint32{
		0x00ffffff, 0x004444ff, 0x0044ff44, 0x00ff8844,
		0x00ffff00, 0x00ff44ff, 0x0044ffff, 0x000088ff,
	}
	// 只有恒亮需要选颜色：呼吸和渐变的配色由风扇自己变化（官方 App 也调不了）
	if snap.Settings.LightMode != lightSteady {
		hint := "呼吸模式的配色由风扇自行变化，无需选择"
		if snap.Settings.LightMode == lightGradient {
			hint = "渐变模式由风扇自动轮换颜色，无需选择"
		}
		g.text(hdc, hint,
			winRect{c4.Left + g.s(16), c4.Top + int32(float64(rowH)*0.50), c4.Right - g.s(16), c4.Top + int32(float64(rowH)*0.80)},
			g.fBody, colMuted, dtSingleLine)
		palette = nil
	}
	sw := int32(float64(rowH) * 0.42)
	if sw > g.s(40) {
		sw = g.s(40)
	}
	sx := c4.Left + g.s(16)
	sy := c4.Top + int32(float64(rowH)*0.50)
	for i, col := range palette {
		r := winRect{sx, sy, sx + sw, sy + sw}
		g.fill(hdc, r, col)
		if i == snap.Settings.ColorSel {
			g.fill(hdc, winRect{r.Left - g.s(2), r.Top - g.s(2), r.Right + g.s(2), r.Top}, colAccent)
			g.fill(hdc, winRect{r.Left - g.s(2), r.Bottom, r.Right + g.s(2), r.Bottom + g.s(2)}, colAccent)
			g.fill(hdc, winRect{r.Left - g.s(2), r.Top, r.Left, r.Bottom}, colAccent)
			g.fill(hdc, winRect{r.Right, r.Top, r.Right + g.s(2), r.Bottom}, colAccent)
		}
		g.hits = append(g.hits, hotRegion{r, fmt.Sprintf("color%d", i)})
		sx += sw + g.s(10)
	}

	// 日志
	logR := winRect{padX, logTop, w - padX, h - padY}
	if logR.Bottom > logR.Top+g.s(40) {
		g.card(hdc, logR)
		g.text(hdc, "通信日志", winRect{logR.Left + g.s(16), logR.Top + g.s(6), logR.Right, logR.Top + g.s(28)},
			g.fSmall, colMuted, dtSingleLine)
		lineH := g.s(17)
		y := logR.Top + g.s(32)
		maxLines := int((logR.Bottom - y - g.s(6)) / lineH)
		logs := snap.Logs
		if len(logs) > maxLines {
			logs = logs[len(logs)-maxLines:]
		}
		for _, e := range logs {
			c := colMuted
			switch e.Dir {
			case "RX":
				c = colOk
			case "ERR":
				c = colDanger
			}
			g.text(hdc, e.T, winRect{logR.Left + g.s(16), y, logR.Left + g.s(104), y + lineH},
				g.fMono, colMuted, dtSingleLine)
			g.text(hdc, e.Dir, winRect{logR.Left + g.s(108), y, logR.Left + g.s(146), y + lineH},
				g.fMono, c, dtSingleLine)
			g.text(hdc, e.Data, winRect{logR.Left + g.s(150), y, logR.Right - g.s(12), y + lineH},
				g.fMono, colText, dtSingleLine|dtEndEllipsis)
			y += lineH
		}
		if len(logs) == 0 {
			g.text(hdc, "（暂无报文）", winRect{logR.Left + g.s(16), y, logR.Right, y + lineH},
				g.fMono, colMuted, dtSingleLine)
		}
	}
}

// ---------- 交互 ----------

func (g *guiApp) hitAt(x, y int32) string {
	for i := len(g.hits) - 1; i >= 0; i-- {
		h := g.hits[i]
		if x >= h.r.Left && x < h.r.Right && y >= h.r.Top && y < h.r.Bottom {
			return h.id
		}
	}
	return ""
}

func (g *guiApp) onMove(x, y int32) {
	id := g.hitAt(x, y)
	if id != g.hoverID {
		g.hoverID = id
		pInvalidateRect.Call(g.hwnd, 0, 0)
	}
	if g.dragging {
		g.setBrightFromX(x)
	}
}

func (g *guiApp) onDown(x, y int32) {
	id := g.hitAt(x, y)
	switch {
	case strings.HasPrefix(id, "bright"):
		g.dragging = true
		g.setBrightFromX(x)
	case id == "conn":
		snap := g.ctl.Snapshot()
		if snap.Connected {
			go g.ctl.Disconnect()
		} else {
			go func() { _ = g.ctl.Connect(g.device) }()
		}
	case strings.HasPrefix(id, "speed"):
		idx := int(id[len("speed")] - '0')
		g.update(func(s *Settings) {
			s.FanAI = false
			s.Speed = idx
		})
	case id == "ai":
		g.update(func(s *Settings) { s.FanAI = !s.FanAI })
	case strings.HasPrefix(id, "light"):
		idx := int(id[len("light")] - '0')
		g.update(func(s *Settings) { s.LightMode = idx })
	case strings.HasPrefix(id, "color"):
		idx := int(id[len("color")] - '0')
		pal := []string{"#ffffff", "#ff4444", "#44ff44", "#4488ff", "#00ffff", "#ff44ff", "#ffff44", "#ff8800"}
		g.update(func(s *Settings) {
			s.Colors = []string{pal[idx]}
			s.ColorSel = 0
		})
	}
	pInvalidateRect.Call(g.hwnd, 0, 0)
}

func (g *guiApp) onUp() {
	if g.dragging {
		g.dragging = false
		v := g.bright
		g.bright = 0
		if v > 0 {
			g.update(func(s *Settings) { s.Brightness = v })
		}
	}
}

// setBrightFromX 把滑块位置换算成 1..9。
func (g *guiApp) setBrightFromX(x int32) {
	for _, h := range g.hits {
		if h.id != "bright" {
			continue
		}
		w := h.r.Right - h.r.Left
		if w <= 0 {
			return
		}
		frac := float64(x-h.r.Left) / float64(w)
		if frac < 0 {
			frac = 0
		}
		if frac > 1 {
			frac = 1
		}
		g.bright = 1 + int(frac*8+0.5)
		pInvalidateRect.Call(g.hwnd, 0, 0)
		return
	}
}

// update 改一份设置副本并下发。
func (g *guiApp) update(mut func(*Settings)) {
	s := g.ctl.Snapshot().Settings
	mut(&s)
	go func() { _ = g.ctl.Apply(s) }()
}

// 保证 time 包被用上（定时器逻辑未来可能用到）
var _ = time.Second

// ---------- 系统托盘 ----------

// notifyIconData 对应 NOTIFYICONDATAW（x64 下 976 字节）。
type notifyIconData struct {
	CbSize           uint32
	_                uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	_                uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         [16]byte
	HBalloonIcon     uintptr
}

// addTray 把图标放进通知区域。
func (g *guiApp) addTray() bool {
	if g.trayIcon == 0 {
		g.trayIcon = loadIcon(32)
		if g.trayIcon == 0 {
			g.trayIcon, _, _ = pLoadIconW.Call(0, idiApplication)
		}
	}
	nid := notifyIconData{
		HWnd:             g.hwnd,
		UID:              idTray,
		UFlags:           nifMessage | nifIcon | nifTip,
		UCallbackMessage: trayMsg,
		HIcon:            g.trayIcon,
	}
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	copy(nid.SzTip[:], syscall.StringToUTF16("风刃F9 控制台"))
	var callErr error
	var r uintptr
	for attempt := 0; attempt < 3; attempt++ {
		r, _, callErr = pShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
		if r != 0 {
			break
		}
		time.Sleep(400 * time.Millisecond)
	}
	g.trayAdded = r != 0
	if g.trayAdded {
		uiLog("托盘图标已添加")
	} else {
		uiLog("托盘图标添加失败: %v（进程完整性 %s）", callErr, tokenIntegrity())
	}
	return g.trayAdded

}

// removeTray 从通知区域移除图标。
func (g *guiApp) removeTray() {
	if !g.trayAdded {
		return
	}
	nid := notifyIconData{HWnd: g.hwnd, UID: idTray}
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	pShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
	g.trayAdded = false
}

// hideToTray 关窗口时藏起来（任务栏图标一并消失）。
func (g *guiApp) hideToTray() {
	pShowWindow.Call(g.hwnd, 0) // SW_HIDE
}

// showFromTray 从托盘恢复窗口。
func (g *guiApp) showFromTray() {
	pShowWindow.Call(g.hwnd, 9) // SW_RESTORE
	pShowWindow.Call(g.hwnd, swShow)
	pSetForegroundWindow.Call(g.hwnd)
}

// showMenu 右键菜单：打开 / 退出。
func (g *guiApp) showMenu() {
	hmenu, _, _ := pCreatePopupMenu.Call()
	if hmenu == 0 {
		return
	}
	defer pDestroyMenu.Call(hmenu)
	open, _ := syscall.UTF16PtrFromString("打开")
	auto, _ := syscall.UTF16PtrFromString("开机启动")
	exit, _ := syscall.UTF16PtrFromString("退出")
	pAppendMenuW.Call(hmenu, mfString, idMenuOpen, uintptr(unsafe.Pointer(open)))
	pAppendMenuW.Call(hmenu, mfSeparator, 0, 0)
	autoFlags := uintptr(mfString)
	if autostartEnabled() {
		autoFlags |= mfChecked
	}
	pAppendMenuW.Call(hmenu, autoFlags, idMenuAuto, uintptr(unsafe.Pointer(auto)))
	pAppendMenuW.Call(hmenu, mfSeparator, 0, 0)
	pAppendMenuW.Call(hmenu, mfString, idMenuExit, uintptr(unsafe.Pointer(exit)))

	var pt winPoint
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// 必须先把自己设成前台，否则菜单点了不消失
	pSetForegroundWindow.Call(g.hwnd)
	cmd, _, _ := pTrackPopupMenu.Call(hmenu, tpmRightBtn|tpmRetCmd,
		uintptr(pt.X), uintptr(pt.Y), 0, g.hwnd, 0)
	pPostMessage.Call(g.hwnd, 0, 0, 0)

	switch cmd {
	case idMenuOpen:
		g.showFromTray()
	case idMenuAuto:
		on := !autostartEnabled()
		if err := setAutostart(on); err != nil {
			msg := "设置开机启动失败：" + err.Error() + "\n\n（需要普通权限；如果程序放在受保护目录，可能被拦）"
			uiLog("开机启动设置失败: %v", err)
			showFatal(msg)
		} else {
			uiLog("开机启动 -> %v", on)
		}
	case idMenuExit:
		g.removeTray()
		pDestroyWindow.Call(g.hwnd)
	}
}

// tokenIntegrity 返回当前进程的完整性级别（Low/Medium/High/System/…）。
// 托盘图标受 UIPI 限制，失败时把这一行打出来方便定位。
func tokenIntegrity() string {
	h, err := syscall.GetCurrentProcess()
	if err != nil {
		return "?"
	}
	var tok syscall.Token
	if err := syscall.OpenProcessToken(h, syscall.TOKEN_QUERY, &tok); err != nil {
		return "?"
	}
	defer tok.Close()

	const tokenIntegrityLevel = 25
	var buf [64]byte
	var need uint32
	r, _, _ := pGetTokenInfo.Call(uintptr(tok), tokenIntegrityLevel,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&need)))
	if r == 0 {
		return "?"
	}

	// TOKEN_MANDATORY_LABEL = { SID_AND_ATTRIBUTES { PSID Sid; DWORD Attributes } }
	sid := *(*unsafe.Pointer)(unsafe.Pointer(&buf[0]))
	if sid == nil {
		return "?"
	}
	// SID: Revision(1) Count(1) IdentifierAuthority(6) SubAuthority[Count](4 each)
	sidBytes := unsafe.Slice((*byte)(sid), 8+4*8)
	count := int(sidBytes[1])
	if count <= 0 || count > 8 {
		return "?"
	}
	off := 8 + 4*(count-1)
	rid := uint32(sidBytes[off]) | uint32(sidBytes[off+1])<<8 |
		uint32(sidBytes[off+2])<<16 | uint32(sidBytes[off+3])<<24
	switch rid {
	case 0x1000:
		return "Low(0x1000)"
	case 0x2000:
		return "Medium(0x2000)"
	case 0x3000:
		return "High(0x3000)"
	case 0x4000:
		return "System(0x4000)"
	}
	return fmt.Sprintf("0x%x", rid)
}
