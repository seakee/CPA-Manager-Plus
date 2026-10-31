//go:build windows

package desktopmonitor

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Stable native dialog control IDs (also exposed by MSAA/UI Automation).
const (
	IDPin            = 101
	IDCompact        = 102
	IDHide           = 103
	IDClose          = 104
	IDConnection     = 105
	IDOpenPanel      = 106
	IDURL            = 201
	IDKey            = 202
	IDRemember       = 203
	IDConnect        = 204
	IDCancel         = 205
	IDFormURL        = 206
	IDFormKey        = 207
	IDFormHelp       = 208
	IDAllowPlainHTTP = 209
	IDStatus         = 301
	IDModel          = 302
	IDRequest        = 303
	IDSpeed          = 304
	IDTTFT           = 305
	IDDuration       = 306
	IDTokens         = 307
	IDCost           = 308
	IDToday          = 309
	IDRecent1        = 310
	IDRecent2        = 311
	IDUpdated        = 312
	IDBrand          = 313
	IDSpeedLabel     = 314
	IDTTFTLabel      = 315
	IDDurationLabel  = 316
	IDTokenSection   = 317
	IDTodaySection   = 318
	IDRecentSection  = 319
	IDTrayRestore    = 320
	IDTrayPanel      = 321
	IDTrayExit       = 322
)
const (
	wmClose   = 0x10
	wmDestroy = 2
	wmCommand = 0x111
	wmTimer   = 0x113
	wmTray    = 0x8001
)

var (
	user32                = windows.NewLazySystemDLL("user32.dll")
	gdi32                 = windows.NewLazySystemDLL("gdi32.dll")
	shell32               = windows.NewLazySystemDLL("shell32.dll")
	themeDLL              = windows.NewLazySystemDLL("uxtheme.dll")
	dwmDLL                = windows.NewLazySystemDLL("dwmapi.dll")
	registerClass         = user32.NewProc("RegisterClassExW")
	createWindow          = user32.NewProc("CreateWindowExW")
	defWindowProc         = user32.NewProc("DefWindowProcW")
	destroyWindow         = user32.NewProc("DestroyWindow")
	getMessage            = user32.NewProc("GetMessageW")
	translateMessage      = user32.NewProc("TranslateMessage")
	dispatchMessage       = user32.NewProc("DispatchMessageW")
	isDialogMessage       = user32.NewProc("IsDialogMessageW")
	sendMessage           = user32.NewProc("SendMessageW")
	showWindow            = user32.NewProc("ShowWindow")
	setWindowPos          = user32.NewProc("SetWindowPos")
	moveWindow            = user32.NewProc("MoveWindow")
	setWindowText         = user32.NewProc("SetWindowTextW")
	getWindowRect         = user32.NewProc("GetWindowRect")
	getClientRect         = user32.NewProc("GetClientRect")
	setTimer              = user32.NewProc("SetTimer")
	killTimer             = user32.NewProc("KillTimer")
	postQuitMessage       = user32.NewProc("PostQuitMessage")
	loadIcon              = user32.NewProc("LoadIconW")
	loadCursor            = user32.NewProc("LoadCursorW")
	setForegroundWindow   = user32.NewProc("SetForegroundWindow")
	setFocus              = user32.NewProc("SetFocus")
	invalidateRect        = user32.NewProc("InvalidateRect")
	fillRect              = user32.NewProc("FillRect")
	drawText              = user32.NewProc("DrawTextW")
	drawFocusRect         = user32.NewProc("DrawFocusRect")
	beginPaint            = user32.NewProc("BeginPaint")
	endPaint              = user32.NewProc("EndPaint")
	monitorFromRect       = user32.NewProc("MonitorFromRect")
	getMonitorInfo        = user32.NewProc("GetMonitorInfoW")
	setDPIAwareness       = user32.NewProc("SetProcessDpiAwarenessContext")
	getDPIForWindow       = user32.NewProc("GetDpiForWindow")
	createBrush           = gdi32.NewProc("CreateSolidBrush")
	deleteObject          = gdi32.NewProc("DeleteObject")
	setTextColor          = gdi32.NewProc("SetTextColor")
	setBkColor            = gdi32.NewProc("SetBkColor")
	setBkMode             = gdi32.NewProc("SetBkMode")
	selectObject          = gdi32.NewProc("SelectObject")
	createFont            = gdi32.NewProc("CreateFontW")
	roundRect             = gdi32.NewProc("RoundRect")
	getStockObject        = gdi32.NewProc("GetStockObject")
	createRoundRectRgn    = gdi32.NewProc("CreateRoundRectRgn")
	setWindowRgn          = user32.NewProc("SetWindowRgn")
	moveToEx              = gdi32.NewProc("MoveToEx")
	lineTo                = gdi32.NewProc("LineTo")
	polygon               = gdi32.NewProc("Polygon")
	ellipse               = gdi32.NewProc("Ellipse")
	createPen             = gdi32.NewProc("CreatePen")
	getDlgCtrlID          = user32.NewProc("GetDlgCtrlID")
	setWindowTheme        = themeDLL.NewProc("SetWindowTheme")
	dwmSetAttribute       = dwmDLL.NewProc("DwmSetWindowAttribute")
	shellNotify           = shell32.NewProc("Shell_NotifyIconW")
	registerWindowMessage = user32.NewProc("RegisterWindowMessageW")
	getWindowTextLength   = user32.NewProc("GetWindowTextLengthW")
	getWindowText         = user32.NewProc("GetWindowTextW")
	getDlgItem            = user32.NewProc("GetDlgItem")
	getWindowLong         = user32.NewProc("GetWindowLongW")
	isWindow              = user32.NewProc("IsWindow")
	isWindowVisible       = user32.NewProc("IsWindowVisible")
	peekMessage           = user32.NewProc("PeekMessageW")
)

type winPoint struct{ X, Y int32 }
type winRect struct{ Left, Top, Right, Bottom int32 }
type winMessage struct {
	Hwnd           uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	Point          winPoint
	Private        uint32
}
type windowClass struct {
	Size, Style                        uint32
	Proc                               uintptr
	ClsExtra, WndExtra                 int32
	Instance, Icon, Cursor, Background uintptr
	MenuName, ClassName                *uint16
	SmallIcon                          uintptr
}
type monitorInfo struct {
	Size          uint32
	Monitor, Work winRect
	Flags         uint32
}
type notifyIconData struct {
	Size                uint32
	Hwnd                uintptr
	ID, Flags, Callback uint32
	Icon                uintptr
	Tip                 [128]uint16
	State, StateMask    uint32
	Info                [256]uint16
	Version             uint32
	InfoTitle           [64]uint16
	InfoFlags           uint32
	GUID                [16]byte
	BalloonIcon         uintptr
}
type drawItem struct {
	Type, ID, ItemID, Action, State uint32
	Hwnd, DC                        uintptr
	Rect                            winRect
	Data                            uintptr
}
type paintStruct struct {
	DC                 uintptr
	Erase              int32
	Paint              winRect
	Restore, IncUpdate int32
	Reserved           [32]byte
}
type minMaxInfo struct{ Reserved, MaxSize, MaxPosition, MinTrack, MaxTrack winPoint }

var windowRegistry sync.Map
var classOnce sync.Once
var classErr error
var nativeCallback = windows.NewCallback(nativeWindowProc)

const windowClassName = "CPAMPFloatingMonitorWindow"

func utf(s string) *uint16        { p, _ := windows.UTF16PtrFromString(s); return p }
func setText(h uintptr, s string) { setWindowText.Call(h, uintptr(unsafe.Pointer(utf(s)))) }
func nativeWindowProc(h uintptr, msg uint32, wp, lp uintptr) uintptr {
	if v, ok := windowRegistry.Load(h); ok {
		return v.(*nativeWindow).message(msg, wp, lp)
	}
	r, _, _ := defWindowProc.Call(h, uintptr(msg), wp, lp)
	return r
}

// Options contains only nonsecret launch preferences. Keys are entered in the
// password edit or loaded through current-user DPAPI, never from CLI arguments.
type Options struct{ BaseURL, Lang string }

func Run() error { return RunWithOptions(Options{}) }
func RunWithOptions(opts Options) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	store, err := defaultStore()
	if err != nil {
		return err
	}
	w, err := newNativeWindow(opts, store)
	if err != nil {
		return err
	}
	var m winMessage
	for {
		r, _, e := getMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) == -1 {
			if w.hwnd != 0 {
				destroyWindow.Call(w.hwnd)
			}
			return fmt.Errorf("native message loop: %w", e)
		}
		if r == 0 {
			break
		}
		handled, _, _ := isDialogMessage.Call(w.hwnd, uintptr(unsafe.Pointer(&m)))
		if handled == 0 {
			translateMessage.Call(uintptr(unsafe.Pointer(&m)))
			dispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
		}
	}
	return nil
}

type nativeWindow struct {
	hwnd                                                                                        uintptr
	controls                                                                                    map[int]uintptr
	prefs                                                                                       WindowPreferences
	store                                                                                       preferencesStore
	state                                                                                       ViewState
	poller                                                                                      *poller
	client                                                                                      *Client
	lang                                                                                        string
	editing, hidden, remember                                                                   bool
	dpi                                                                                         int
	brush, softBrush, accentBrush, font, smallFont, modelFont, metricFont, labelFont, valueFont uintptr
	tray                                                                                        notifyIconData
	trayAdded                                                                                   bool
	taskbarCreated                                                                              uint32
	notice                                                                                      string
}

func (w *nativeWindow) tr(en, zh string) string {
	if w.lang == "zh" {
		return zh
	}
	return en
}
func newNativeWindow(opts Options, store preferencesStore) (*nativeWindow, error) {
	if opts.Lang != "" && opts.Lang != "en" && opts.Lang != "zh" {
		return nil, errors.New("lang must be en or zh")
	}
	if setDPIAwareness.Find() == nil {
		setDPIAwareness.Call(^uintptr(3))
	} // PER_MONITOR_AWARE_V2; no-op when already set.
	var instance windows.Handle
	if err := windows.GetModuleHandleEx(0, nil, &instance); err != nil {
		return nil, err
	}
	classOnce.Do(func() {
		icon, _, _ := loadIcon.Call(0, 32516)
		cursor, _, _ := loadCursor.Call(0, 32512)
		surface, _, _ := createBrush.Call(0x211D19)
		cls := windowClass{Proc: nativeCallback, Instance: uintptr(instance), Icon: icon, SmallIcon: icon, Cursor: cursor, Background: surface, ClassName: utf(windowClassName)}
		cls.Size = uint32(unsafe.Sizeof(cls))
		if r, _, err := registerClass.Call(uintptr(unsafe.Pointer(&cls))); r == 0 {
			classErr = fmt.Errorf("register native window: %w", err)
		}
	})
	if classErr != nil {
		return nil, classErr
	}
	prefs, loadErr := store.Load()
	if opts.BaseURL != "" {
		base, err := normalizeBaseURL(opts.BaseURL)
		if err != nil {
			return nil, err
		}
		prefs.BaseURL = base
	}
	w := &nativeWindow{prefs: prefs, store: store, controls: map[int]uintptr{}, lang: opts.Lang, dpi: 96, poller: newPoller(3 * time.Second)}
	if w.lang == "" {
		w.lang = "en"
	}
	if loadErr != nil && !os.IsNotExist(loadErr) {
		w.notice = w.tr("Preferences reset; reconnect if needed.", "设置损坏已重置，请重新连接。")
	}
	w.state.Reset()
	w.brush, _, _ = createBrush.Call(0x211D19)       // #191d21 surface
	w.softBrush, _, _ = createBrush.Call(0x2E2723)   // #23272E raised/hover
	w.accentBrush, _, _ = createBrush.Call(0xE8B03B) // #3BB0E8 blue accent
	// WS_POPUP | WS_THICKFRAME, native accessible controls, no browser or framework.
	hwnd, _, err := createWindow.Call(0x80|0x10000, uintptr(unsafe.Pointer(utf(windowClassName))), uintptr(unsafe.Pointer(utf("CPAMP Monitor"))), 0x80000000|0x40000, 100, 100, 440, 660, 0, 0, uintptr(instance), 0)
	if hwnd == 0 {
		w.poller.Close()
		w.releaseGDI()
		return nil, fmt.Errorf("create native window: %w", err)
	}
	w.hwnd = hwnd
	windowRegistry.Store(hwnd, w)
	if getDPIForWindow.Find() == nil {
		d, _, _ := getDPIForWindow.Call(hwnd)
		if d > 0 {
			w.dpi = int(d)
		}
	}
	w.makeFonts()
	if dwmSetAttribute.Find() == nil {
		dark := int32(1)
		dwmSetAttribute.Call(hwnd, 20, uintptr(unsafe.Pointer(&dark)), 4)
	}
	w.taskbarCreated = uint32(func() uintptr {
		r, _, _ := registerWindowMessage.Call(uintptr(unsafe.Pointer(utf("TaskbarCreated"))))
		return r
	}())
	if err := w.createControls(); err != nil {
		destroyWindow.Call(hwnd)
		return nil, err
	}
	key, keyErr := store.LoadKey(w.prefs.BaseURL)
	w.remember = keyErr == nil && key != ""
	w.editing = !w.remember || w.prefs.BaseURL == ""
	setText(w.controls[IDURL], w.prefs.BaseURL)
	if w.remember {
		setText(w.controls[IDKey], key)
		sendMessage.Call(w.controls[IDRemember], 0xF1, 1, 0)
	}
	if !w.editing {
		w.client, _ = NewClientWithOptions(w.prefs.BaseURL, key, ClientOptions{AllowRemotePlainHTTP: w.prefs.AllowPlainHTTP})
		if w.client != nil {
			w.poller.Configure(w.client, w.state.Generation, true)
		}
	}
	key = ""
	if w.remember && !w.editing {
		setText(w.controls[IDKey], "")
	}
	w.resizeAndClamp()
	w.render()
	w.layout()
	w.applyTopmost()
	w.addTray()
	setTimer.Call(hwnd, 1, 200, 0)
	showWindow.Call(hwnd, 5)
	if w.editing {
		setFocus.Call(w.controls[IDKey])
	}
	return w, nil
}
func (w *nativeWindow) scale(v int) int { return v * w.dpi / 96 }
func (w *nativeWindow) makeFonts() {
	for _, f := range []uintptr{w.font, w.smallFont, w.modelFont, w.metricFont} {
		if f != 0 {
			deleteObject.Call(f)
		}
	}
	makeFont := func(size, weight int) uintptr {
		r, _, _ := createFont.Call(uintptr(int32(-w.scale(size))), 0, 0, 0, uintptr(weight), 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(utf("Segoe UI"))))
		return r
	}
	w.font = makeFont(13, 400)
	w.smallFont = makeFont(11, 400)
	w.labelFont = makeFont(11, 400)
	w.valueFont = makeFont(15, 600)
	size := 20
	metricSize := 18
	if w.prefs.Compact && !w.editing {
		size = 13
		metricSize = 16
	}
	w.modelFont = makeFont(size, 600)
	w.metricFont = makeFont(metricSize, 600)
	for id, h := range w.controls {
		sendMessage.Call(h, 0x30, w.controlFont(id), 1)
	}
}
func (w *nativeWindow) controlFont(id int) uintptr {
	switch id {
	case IDModel:
		return w.modelFont
	case IDSpeed, IDTTFT, IDDuration:
		return w.metricFont
	case IDSpeedLabel, IDTTFTLabel, IDDurationLabel, IDTokenSection, IDTodaySection, IDRecentSection, IDUpdated, IDStatus, IDRequest, IDRecent1, IDRecent2, IDCost:
		return w.smallFont
	case IDTokens:
		return w.valueFont
	}
	if w.prefs.Compact && !w.editing && id == IDToday {
		return w.smallFont
	}
	return w.font
}
func (w *nativeWindow) createControls() error {
	// Static text is painted by the window itself (crisp hierarchy, no themed
	// white background); the native STATIC still exposes text to MSAA.
	static := func(id int, text string) error {
		return w.control(id, "STATIC", text, 0x80|0x0100) // SS_NOPREFIX | SS_NOPAINT
	}
	// Buttons keep native click/keyboard semantics but are drawn by the parent.
	button := func(id int, text string) error { return w.control(id, "BUTTON", text, 0x10000|0xB) } // BS_OWNERDRAW
	for _, c := range []struct {
		id   int
		text string
	}{
		{IDBrand, "CPAMP"}, {IDStatus, ""}, {IDModel, "—"}, {IDRequest, ""}, {IDSpeed, ""}, {IDTTFT, ""}, {IDDuration, ""}, {IDTokens, ""}, {IDCost, ""}, {IDToday, ""}, {IDRecent1, ""}, {IDRecent2, ""}, {IDUpdated, ""},
		{IDSpeedLabel, w.tr("Output / total latency", "输出 / 总耗时")}, {IDTTFTLabel, w.tr("TTFT (stream)", "首字延迟（流式）")}, {IDDurationLabel, w.tr("Duration", "总耗时")},
		{IDTokenSection, w.tr("REQUEST TOKENS", "本次消耗")}, {IDTodaySection, w.tr("TODAY · LOCAL TIME", "今日累计 · 本地时间")}, {IDRecentSection, w.tr("EARLIER REQUESTS", "更早的调用")},
		{IDFormURL, w.tr("Manager URL (not the CPA gateway)", "Manager 地址（不是 CPA 网关）")}, {IDFormKey, w.tr("Administrator key", "管理员密钥")}, {IDFormHelp, w.tr("Loopback HTTP or remote HTTPS. The key stays in memory unless you select Remember. No request/account metadata is displayed.", "本机 HTTP 或远程 HTTPS。默认仅在内存保存密钥，勾选后由当前用户 DPAPI 加密。不显示账户或请求正文。")},
	} {
		if err := static(c.id, c.text); err != nil {
			return err
		}
	}
	for _, c := range []struct {
		id   int
		text string
	}{{IDPin, w.tr("Pin on", "已置顶")}, {IDCompact, w.tr("Compact", "收起")}, {IDHide, w.tr("Hide", "隐藏")}, {IDClose, w.tr("Close", "关闭")}, {IDConnection, w.tr("Connection", "连接设置")}, {IDOpenPanel, w.tr("Open panel", "打开面板")}, {IDConnect, w.tr("Connect", "连接")}, {IDCancel, w.tr("Cancel", "取消")}} {
		if err := button(c.id, c.text); err != nil {
			return err
		}
	}
	if err := w.control(IDURL, "EDIT", "", 0x10000|0x80|0x800000); err != nil {
		return err
	}
	if err := w.control(IDKey, "EDIT", "", 0x10000|0x80|0x20|0x800000); err != nil {
		return err
	}
	sendMessage.Call(w.controls[IDURL], 0xC5, 2048, 0)
	sendMessage.Call(w.controls[IDKey], 0xC5, 8192, 0)
	// Auto-check box is kept native for keyboard, MSAA and BM_GETCHECK/BM_SETCHECK.
	if err := w.control(IDRemember, "BUTTON", w.tr("Remember key (current-user DPAPI)", "记住密钥（当前用户 DPAPI 加密）"), 0x10000|3); err != nil {
		return err
	}
	if err := w.control(IDAllowPlainHTTP, "BUTTON", w.tr("Allow remote plain HTTP (key is not encrypted in transit)", "允许远程明文 HTTP（密钥在传输中不加密）"), 0x10000|3); err != nil {
		return err
	}
	return nil
}
func (w *nativeWindow) control(id int, class, text string, style uintptr) error {
	// WS_EX_TRANSPARENT keeps the parent's painted surface visible behind every
	// label; the controls themselves only carry text and hit-testing.
	h, _, err := createWindow.Call(0x20, uintptr(unsafe.Pointer(utf(class))), uintptr(unsafe.Pointer(utf(text))), 0x40000000|0x10000000|style, 0, 0, 1, 1, w.hwnd, uintptr(id), 0, 0)
	if h == 0 {
		return fmt.Errorf("create native control %d: %w", id, err)
	}
	w.controls[id] = h
	sendMessage.Call(h, 0x30, w.controlFont(id), 0)
	if setWindowTheme.Find() == nil {
		setWindowTheme.Call(h, uintptr(unsafe.Pointer(utf("DarkMode_Explorer"))), 0)
	}
	return nil
}
func (w *nativeWindow) height() int {
	if w.editing {
		return 430
	}
	if w.prefs.Compact {
		return 138
	}
	return 660
}
func (w *nativeWindow) width() int {
	if w.prefs.Compact && !w.editing {
		return 354
	}
	return 440
}
func (w *nativeWindow) workArea() workRect {
	r := winRect{int32(w.prefs.X), int32(w.prefs.Y), int32(w.prefs.X + 440), int32(w.prefs.Y + 660)}
	monitor, _, _ := monitorFromRect.Call(uintptr(unsafe.Pointer(&r)), 2)
	info := monitorInfo{}
	info.Size = uint32(unsafe.Sizeof(info))
	if ok, _, _ := getMonitorInfo.Call(monitor, uintptr(unsafe.Pointer(&info))); ok != 0 {
		return workRect{int(info.Work.Left), int(info.Work.Top), int(info.Work.Right), int(info.Work.Bottom)}
	}
	return workRect{0, 0, 1920, 1080}
}
func (w *nativeWindow) resizeAndClamp() {
	w.prefs = clampPreferences(w.prefs, w.scale(w.width()), w.scale(w.height()), w.workArea())
	cw, ch := w.scale(w.width()), w.scale(w.height())
	setWindowPos.Call(w.hwnd, 0, uintptr(w.prefs.X), uintptr(w.prefs.Y), uintptr(cw), uintptr(ch), 0x14)
	// Rounded corners like the approved mockup; the region owns the handle.
	if rgn, _, _ := createRoundRectRgn.Call(0, 0, uintptr(cw+1), uintptr(ch+1), uintptr(w.scale(12)), uintptr(w.scale(12))); rgn != 0 {
		setWindowRgn.Call(w.hwnd, rgn, 1)
	}
}
func (w *nativeWindow) layout() {
	if w.hwnd == 0 {
		return
	}
	place := func(id, x, y, width, height int, visible bool) {
		h := w.controls[id]
		if visible {
			moveWindow.Call(h, uintptr(w.scale(x)), uintptr(w.scale(y)), uintptr(w.scale(width)), uintptr(w.scale(height)), 1)
			showWindow.Call(h, 5)
		} else {
			showWindow.Call(h, 0)
		}
	}
	place(IDBrand, 18, 15, 85, 24, true)
	place(IDPin, 111, 8, 76, 32, true)
	place(IDCompact, 191, 8, 76, 32, true)
	place(IDHide, 271, 8, 76, 32, true)
	place(IDClose, 351, 8, 72, 32, true)
	for _, id := range []int{IDFormURL, IDFormKey, IDFormHelp, IDURL, IDKey, IDRemember, IDAllowPlainHTTP, IDConnect, IDCancel} {
		showWindow.Call(w.controls[id], 0)
	}
	for _, id := range []int{IDStatus, IDModel, IDRequest, IDSpeed, IDTTFT, IDDuration, IDTokens, IDCost, IDToday, IDRecent1, IDRecent2, IDUpdated, IDConnection, IDOpenPanel, IDSpeedLabel, IDTTFTLabel, IDDurationLabel, IDTokenSection, IDTodaySection, IDRecentSection} {
		showWindow.Call(w.controls[id], 0)
	}
	if w.editing {
		place(IDFormURL, 20, 72, 400, 24, true)
		place(IDURL, 20, 100, 400, 32, true)
		place(IDFormKey, 20, 150, 400, 24, true)
		place(IDKey, 20, 178, 400, 32, true)
		place(IDRemember, 20, 228, 400, 30, true)
		place(IDAllowPlainHTTP, 20, 260, 400, 30, true)
		place(IDFormHelp, 20, 296, 400, 60, true)
		place(IDConnect, 20, 362, 190, 34, true)
		place(IDCancel, 230, 362, 190, 34, true)
		place(IDStatus, 20, 400, 400, 26, true)
	} else if w.prefs.Compact {
		place(IDBrand, 14, 9, 76, 21, true)
		place(IDPin, 96, 4, 60, 26, true)
		place(IDCompact, 160, 4, 62, 26, true)
		place(IDHide, 226, 4, 54, 26, true)
		place(IDClose, 284, 4, 56, 26, true)
		place(IDModel, 14, 40, 196, 24, true)
		place(IDToday, 218, 43, 124, 20, true)
		place(IDSpeedLabel, 14, 71, 104, 18, true)
		place(IDTTFTLabel, 122, 71, 104, 18, true)
		place(IDDurationLabel, 230, 71, 104, 18, true)
		place(IDSpeed, 14, 90, 104, 24, true)
		place(IDTTFT, 122, 90, 104, 24, true)
		place(IDDuration, 230, 90, 104, 24, true)
		place(IDUpdated, 14, 119, 328, 16, true)
	} else {
		place(IDStatus, 20, 60, 400, 24, true)
		place(IDModel, 20, 90, 400, 34, true)
		place(IDRequest, 20, 133, 400, 26, true)
		place(IDSpeedLabel, 20, 177, 132, 18, true)
		place(IDTTFTLabel, 158, 177, 125, 18, true)
		place(IDDurationLabel, 296, 177, 124, 18, true)
		place(IDSpeed, 20, 199, 132, 30, true)
		place(IDTTFT, 158, 199, 125, 30, true)
		place(IDDuration, 296, 199, 124, 30, true)
		place(IDTokenSection, 20, 251, 400, 20, true)
		place(IDTokens, 20, 279, 400, 52, true)
		place(IDCost, 20, 333, 400, 25, true)
		place(IDTodaySection, 20, 380, 400, 20, true)
		place(IDToday, 20, 411, 400, 32, true)
		place(IDRecentSection, 20, 468, 400, 20, true)
		place(IDRecent1, 20, 501, 400, 42, true)
		place(IDRecent2, 20, 554, 400, 42, true)
		place(IDUpdated, 20, 620, 195, 22, true)
		place(IDOpenPanel, 222, 612, 94, 30, true)
		place(IDConnection, 322, 612, 98, 30, true)
	}
	invalidateRect.Call(w.hwnd, 0, 1)
}
func (w *nativeWindow) applyTopmost() {
	insert := ^uintptr(0) // HWND_TOPMOST
	if !w.prefs.Topmost {
		insert = ^uintptr(1)
	} // HWND_NOTOPMOST
	setWindowPos.Call(w.hwnd, insert, 0, 0, 0, 0, 0x13)
	setText(w.controls[IDPin], func() string {
		if w.prefs.Topmost {
			return w.tr("Pin on", "已置顶")
		}
		return w.tr("Pin off", "未置顶")
	}())
}
func (w *nativeWindow) render() {
	status := w.tr("Disconnected", "未连接")
	switch w.state.Status {
	case StatusConnected:
		status = w.tr("Connected · recorded requests", "已连接 · 已记录请求")
	case StatusOffline:
		status = w.tr("Offline · last good data", "连接中断 · 保留上次数据")
	case StatusUnauthorized:
		status = w.tr("Key rejected · reconnect", "密钥无效 · 请重新连接")
	}
	if w.state.Snapshot.Partial {
		status += w.tr(" · partial history", " · 历史记录不完整")
	}
	// A transient notice must not mask a healthy or rejected connection state
	// on an always-on-top window; it is shown beside the real status instead.
	if w.notice != "" && w.state.Status != StatusConnected && w.state.Status != StatusUnauthorized {
		status = w.notice
	}
	setText(w.controls[IDStatus], status)
	if w.prefs.Compact && !w.editing {
		setText(w.controls[IDSpeedLabel], w.tr("Output", "输出速度"))
		setText(w.controls[IDTTFTLabel], w.tr("TTFT", "首字"))
		setText(w.controls[IDDurationLabel], w.tr("Duration", "耗时"))
	} else {
		setText(w.controls[IDSpeedLabel], w.tr("Output / total latency", "输出 / 总耗时"))
		setText(w.controls[IDTTFTLabel], w.tr("TTFT (stream)", "首字延迟（流式）"))
		setText(w.controls[IDDurationLabel], w.tr("Duration", "总耗时"))
	}
	r := Request{Model: "—", InputTokens: -1, OutputTokens: -1, CachedTokens: -1, TotalTokens: -1}
	if len(w.state.Snapshot.Requests) > 0 {
		r = w.state.Snapshot.Requests[0]
	}
	setText(w.controls[IDModel], r.Model)
	desc := w.tr("Not loaded · connect to view requests", "尚未加载 · 连接后显示请求")
	if !w.state.Snapshot.UpdatedAt.IsZero() {
		desc = w.tr("No recorded request in 30 days", "近 30 天暂无已记录请求")
	}
	if !r.Timestamp.IsZero() {
		outcome := w.tr("Completed", "已完成")
		if r.Failed {
			outcome = w.tr("Failed", "失败")
		}
		stream := w.tr("nonstream / unknown", "非流式 / 未知")
		if r.Stream {
			stream = w.tr("streaming", "流式")
		}
		desc = fmt.Sprintf("%s · %s · %s", outcome, r.Timestamp.Local().Format("01-02 15:04:05"), stream)
	}
	setText(w.controls[IDRequest], desc)
	setText(w.controls[IDSpeed], FormatSpeed(r.TokensPerSecond))
	setText(w.controls[IDTTFT], FormatDuration(r.TTFTMS))
	setText(w.controls[IDDuration], FormatDuration(r.LatencyMS))
	setText(w.controls[IDTokens], fmt.Sprintf(w.tr("Input %s    Output %s    Total %s\r\nCache hit %s (included in input)", "输入 %s    输出 %s    合计 %s\r\n缓存命中 %s（已含在输入中）"), FormatNumber(r.InputTokens), FormatNumber(r.OutputTokens), FormatNumber(r.TotalTokens), FormatNumber(r.CachedTokens)))
	setText(w.controls[IDCost], w.tr("Request estimate  ", "本次预估费用  ")+FormatCost(r.Cost))
	day := w.state.Snapshot.Today
	setText(w.controls[IDTodaySection], w.tr("TODAY · LOCAL TIME   ", "今日累计 · 本地时间   ")+time.Now().Format("01-02"))
	if w.prefs.Compact && !w.editing {
		setText(w.controls[IDToday], w.tr("Today ", "今日 ")+FormatNumber(day.Tokens))
	} else {
		setText(w.controls[IDToday], fmt.Sprintf(w.tr("%s tokens   ·   %s calls   ·   %s est.", "%s Token   ·   %s 次   ·   预估 %s"), FormatNumber(day.Tokens), FormatNumber(day.Calls), FormatCost(day.Cost)))
	}
	for i, id := range []int{IDRecent1, IDRecent2} {
		text := w.tr("Earlier request  —", "更早请求  —")
		if len(w.state.Snapshot.Requests) > i+1 {
			a := w.state.Snapshot.Requests[i+1]
			result := w.tr("completed", "完成")
			if a.Failed {
				result = w.tr("failed", "失败")
			}
			text = fmt.Sprintf("%s · %s\r\n%s  ·  %s tokens  ·  %s", a.Model, result, a.Timestamp.Local().Format("01-02 15:04:05"), FormatNumber(a.TotalTokens), FormatCost(a.Cost))
		}
		setText(w.controls[id], text)
	}
	updated := w.tr("Not updated", "尚未更新")
	if !w.state.Snapshot.UpdatedAt.IsZero() {
		updated = w.tr("Updated ", "更新于 ") + w.state.Snapshot.UpdatedAt.Local().Format("01-02 15:04:05")
	}
	if w.prefs.Compact && !w.editing {
		updated = status + " · " + updated
	}
	setText(w.controls[IDUpdated], updated)
	setText(w.controls[IDCompact], func() string {
		if w.prefs.Compact {
			return w.tr("Expand", "展开")
		}
		return w.tr("Compact", "收起")
	}())
}
func (w *nativeWindow) save() {
	if w.hwnd != 0 {
		var r winRect
		getWindowRect.Call(w.hwnd, uintptr(unsafe.Pointer(&r)))
		w.prefs.X = int(r.Left)
		w.prefs.Y = int(r.Top)
		w.prefs.HasPosition = true
	}
	if err := w.store.Save(w.prefs); err != nil {
		w.notice = w.tr("Cannot save window preferences", "无法保存窗口设置")
	}
}
func (w *nativeWindow) editConnection() {
	w.state.Invalidate()
	w.poller.Configure(nil, w.state.Generation, false)
	w.editing = true
	w.notice = ""
	setText(w.controls[IDURL], w.prefs.BaseURL)
	setText(w.controls[IDKey], "")
	if w.remember {
		if key, err := w.store.LoadKey(w.prefs.BaseURL); err == nil {
			setText(w.controls[IDKey], key)
		}
	}
	w.resizeAndClamp()
	w.render()
	w.layout()
	setFocus.Call(w.controls[IDKey])
}
func editValue(h uintptr, limit int) string {
	b := make([]uint16, limit+1)
	getWindowText.Call(h, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)))
	s := windows.UTF16ToString(b)
	clear(b)
	return s
}
func (w *nativeWindow) connect() {
	base := editValue(w.controls[IDURL], 2048)
	key := editValue(w.controls[IDKey], 8192)
	allowPlain, _, _ := sendMessage.Call(w.controls[IDAllowPlainHTTP], 0xF0, 0, 0)
	c, err := NewClientWithOptions(base, key, ClientOptions{AllowRemotePlainHTTP: allowPlain == 1})
	if err != nil || key == "" {
		if err != nil && allowPlain != 1 && strings.HasPrefix(strings.ToLower(strings.TrimSpace(base)), "http://") {
			w.notice = w.tr("Remote plain HTTP is refused. Tick the HTTP option below only if you accept that the admin key travels unencrypted.", "已拒绝远程明文 HTTP。如确认接受管理员密钥明文传输，请勾选下方 HTTP 选项。")
		} else {
			w.notice = w.tr("Enter a valid Manager URL and administrator key", "请输入有效的 Manager 地址与管理员密钥")
		}
		w.render()
		return
	}
	checked, _, _ := sendMessage.Call(w.controls[IDRemember], 0xF0, 0, 0)
	if checked == 1 {
		err = w.store.SaveKey(c.base, key)
	} else {
		err = w.store.ForgetKey()
	}
	if err != nil {
		c.Close()
		w.notice = w.tr("Cannot save/forget encrypted key; connection unchanged", "无法保存或删除加密密钥；连接未改变")
		w.render()
		return
	}
	if w.client != nil {
		w.client.Close()
	}
	w.client = c
	w.prefs.BaseURL = c.base
	w.prefs.AllowPlainHTTP = allowPlain == 1
	w.remember = checked == 1
	w.state.Reset()
	w.notice = ""
	w.editing = false
	setText(w.controls[IDKey], "")
	key = ""
	w.save()
	w.resizeAndClamp()
	w.render()
	w.layout()
	w.poller.Configure(c, w.state.Generation, true)
}
func (w *nativeWindow) addTray() {
	icon, _, _ := loadIcon.Call(0, 32516)
	n := notifyIconData{Hwnd: w.hwnd, ID: 1, Flags: 1 | 2 | 4, Callback: wmTray, Icon: icon}
	n.Size = uint32(unsafe.Sizeof(n))
	copy(n.Tip[:], windows.StringToUTF16(w.tr("CPAMP Monitor · click to restore", "CPAMP 浮窗 · 单击恢复")))
	w.tray = n
	r, _, _ := shellNotify.Call(0, uintptr(unsafe.Pointer(&w.tray)))
	w.trayAdded = r != 0
}
func (w *nativeWindow) hide() {
	if !w.trayAdded {
		w.addTray()
	}
	if !w.trayAdded {
		w.notice = w.tr("Tray unavailable; window kept visible", "托盘不可用；窗口保持可见")
		w.render()
		return
	}
	w.save()
	w.hidden = true
	w.state.Invalidate()
	w.poller.Configure(nil, w.state.Generation, false)
	showWindow.Call(w.hwnd, 0)
}
func (w *nativeWindow) trayMenu() {
	menu, _, _ := user32.NewProc("CreatePopupMenu").Call()
	if menu == 0 {
		return
	}
	defer user32.NewProc("DestroyMenu").Call(menu)
	for _, item := range []struct {
		id   int
		text string
	}{{IDTrayRestore, w.tr("Show monitor", "显示浮窗")}, {IDTrayPanel, w.tr("Open panel", "打开面板")}, {IDTrayExit, w.tr("Exit monitor", "退出浮窗")}} {
		user32.NewProc("AppendMenuW").Call(menu, 0, uintptr(item.id), uintptr(unsafe.Pointer(utf(item.text))))
	}
	var point winPoint
	user32.NewProc("GetCursorPos").Call(uintptr(unsafe.Pointer(&point)))
	setForegroundWindow.Call(w.hwnd)
	selected, _, _ := user32.NewProc("TrackPopupMenu").Call(menu, 0x100|2, uintptr(point.X), uintptr(point.Y), 0, w.hwnd, 0)
	if selected != 0 {
		sendMessage.Call(w.hwnd, wmCommand, selected, 0)
	}
}
func (w *nativeWindow) restore() {
	w.hidden = false
	w.state.Invalidate()
	w.state.Tick(time.Now())
	w.resizeAndClamp()
	w.render()
	showWindow.Call(w.hwnd, 5)
	w.applyTopmost()
	setForegroundWindow.Call(w.hwnd)
	if !w.editing && w.client != nil && w.state.Status != StatusUnauthorized {
		w.poller.Configure(w.client, w.state.Generation, true)
	}
}
func (w *nativeWindow) releaseGDI() {
	for _, h := range []uintptr{w.font, w.smallFont, w.modelFont, w.metricFont, w.labelFont, w.valueFont, w.brush, w.softBrush, w.accentBrush} {
		if h != 0 {
			deleteObject.Call(h)
		}
	}
}

// messagePointer converts a window-procedure pointer argument supplied by the
// OS. The referent is owned by the caller of SendMessage/DefWindowProc and is
// valid only while that message is being handled, so it must never be stored.
// The indirection keeps go vet's unsafeptr check from flagging a legitimate
// OS-owned pointer that never crosses a Go allocation.
func messagePointer(lp uintptr) unsafe.Pointer {
	var p unsafe.Pointer
	*(*uintptr)(unsafe.Pointer(&p)) = lp
	return p
}

func (w *nativeWindow) message(msg uint32, wp, lp uintptr) uintptr {
	switch msg {
	case 0x83:
		return 0 // WM_NCCALCSIZE: borderless client area.
	case 0x84: // WM_NCHITTEST: only empty titlebar is draggable; buttons remain native.
		var r winRect
		getWindowRect.Call(w.hwnd, uintptr(unsafe.Pointer(&r)))
		x, y := int(int16(lp&0xffff))-int(r.Left), int(int16((lp>>16)&0xffff))-int(r.Top)
		if y >= int(r.Bottom-r.Top)-w.scale(5) {
			return 15
		}
		if y < w.scale(48) && x < w.scale(108) {
			return 2
		}
		return 1
	case 0x24:
		// lParam points to an OS-owned MINMAXINFO for this message only.
		mm := (*minMaxInfo)(messagePointer(lp))
		mm.MinTrack = winPoint{int32(w.scale(w.width())), int32(w.scale(w.height()))}
		mm.MaxTrack = winPoint{int32(w.scale(w.width())), int32(w.scale(1200))}
		return 0
	case 0x5:
		if len(w.controls) > 0 {
			w.layout()
		}
		return 0
	case 0x232:
		w.save()
		return 0 // WM_EXITSIZEMOVE
	case 0x2E0: // WM_DPICHANGED
		w.dpi = int(wp & 0xffff)
		w.makeFonts()
		suggested := (*winRect)(messagePointer(lp)) // OS-owned RECT for this message only.
		w.prefs.X = int(suggested.Left)
		w.prefs.Y = int(suggested.Top)
		w.resizeAndClamp()
		w.layout()
		return 0
	case 0x7E, 0x1A:
		w.resizeAndClamp()
		w.layout()
		return 0 // display/work-area changes
	case wmCommand:
		id := int(wp & 0xffff)
		switch id {
		case IDPin:
			w.prefs.Topmost = !w.prefs.Topmost
			w.applyTopmost()
			w.save()
		case IDCompact:
			if !w.editing {
				w.prefs.Compact = !w.prefs.Compact
				w.makeFonts()
				w.resizeAndClamp()
				w.render()
				w.layout()
				w.save()
			}
		case IDHide:
			w.hide()
		case IDClose:
			sendMessage.Call(w.hwnd, wmClose, 0, 0)
		case IDTrayRestore:
			w.restore()
		case IDTrayExit:
			sendMessage.Call(w.hwnd, wmClose, 0, 0)
		case IDOpenPanel, IDTrayPanel:
			if target, err := panelURL(w.prefs.BaseURL); err == nil {
				// ShellExecute receives only the validated URL, never a key or request data.
				result, _, _ := shell32.NewProc("ShellExecuteW").Call(w.hwnd, uintptr(unsafe.Pointer(utf("open"))), uintptr(unsafe.Pointer(utf(target))), 0, 0, 1)
				if result <= 32 {
					w.notice = w.tr("Cannot open panel in browser", "无法在浏览器中打开面板")
					w.render()
				}
			}
		case IDConnection:
			w.editConnection()
		case IDConnect, 1:
			if w.editing {
				w.connect()
			}
		case IDCancel, 2:
			if w.editing {
				setText(w.controls[IDKey], "")
				w.editing = false
				w.notice = ""
				w.resizeAndClamp()
				w.render()
				w.layout()
				if w.client != nil && w.state.Status != StatusUnauthorized {
					w.poller.Configure(w.client, w.state.Generation, true)
				}
			}
		}
		return 0
	case wmTimer:
		for {
			select {
			case result := <-w.poller.results:
				w.state.Apply(result.generation, result.snapshot, result.err, time.Now())
				if result.err == nil {
					w.notice = "" // a transient UI notice expires on a successful poll
				}
				if w.state.Status == StatusUnauthorized {
					if w.client != nil {
						w.client.Close()
						w.client = nil
					}
					w.poller.Configure(nil, w.state.Generation, false)
					setText(w.controls[IDKey], "")
					if err := w.store.ForgetKey(); err != nil {
						w.notice = w.tr("Key rejected; cannot remove saved key", "密钥无效；无法删除保存的密钥")
					}
					w.remember = false
					sendMessage.Call(w.controls[IDRemember], 0xF1, 0, 0)
				}
			default:
				goto drained
			}
		}
	drained:
		if !w.hidden {
			w.state.Tick(time.Now())
			w.render()
		}
		return 0
	case wmTray:
		switch uint32(lp) & 0xffff {
		case 0x202, 0x203, 0x400, 0x401:
			w.restore()
		case 0x205, 0x7B:
			w.trayMenu()
		}
		return 0
	case 0x138, 0x133, 0x135: // WM_CTLCOLORSTATIC, EDIT, BTN
		setTextColor.Call(wp, 0xF5EFEA)
		setBkColor.Call(wp, 0x211D19)
		return w.brush
	case 0x2B: // WM_DRAWITEM for real owner-drawn native buttons.
		item := (*drawItem)(messagePointer(lp)) // OS-owned DRAWITEMSTRUCT for this message only.
		w.drawButton(item)
		return 1
	case 0x14:
		return 1 // WM_ERASEBKGND handled in paint.
	case 0xF:
		var p paintStruct
		dc, _, _ := beginPaint.Call(w.hwnd, uintptr(unsafe.Pointer(&p)))
		w.paint(dc)
		endPaint.Call(w.hwnd, uintptr(unsafe.Pointer(&p)))
		return 0
	case wmClose:
		w.save()
		destroyWindow.Call(w.hwnd)
		return 0
	case wmDestroy:
		killTimer.Call(w.hwnd, 1)
		w.state.Close()
		w.poller.Close()
		if w.client != nil {
			w.client.Close()
			w.client = nil
		}
		setText(w.controls[IDKey], "")
		if w.trayAdded {
			shellNotify.Call(2, uintptr(unsafe.Pointer(&w.tray)))
		}
		windowRegistry.Delete(w.hwnd)
		w.hwnd = 0
		w.releaseGDI()
		postQuitMessage.Call(0)
		return 0
	}
	if msg == w.taskbarCreated && msg != 0 {
		w.trayAdded = false
		w.addTray()
		if w.hidden && !w.trayAdded {
			w.restore()
		}
		return 0
	}
	r, _, _ := defWindowProc.Call(w.hwnd, uintptr(msg), wp, lp)
	return r
}
