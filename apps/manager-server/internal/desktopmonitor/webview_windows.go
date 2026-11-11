//go:build windows

package desktopmonitor

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"github.com/jchv/go-webview2/pkg/edge"
	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

const wmWidgetAction = 0x8002

// Only the embedded about:blank document may invoke native actions. No Manager
// page, URL or untrusted remote content is ever navigated inside this WebView.
type widgetBrowser struct {
	chromium          *edge.Chromium
	core              unsafe.Pointer
	owner             *nativeWindow
	guards            []*webEventHandler
	ready             bool
	shown             bool
	initialNavigation bool
	last              string
	pending           []widgetMessage
}
type widgetMessage struct {
	Action    string `json:"action"`
	Base      string `json:"base"`
	Key       string `json:"key"`
	KeepKey   bool   `json:"keepKey"`
	Remember  bool   `json:"remember"`
	AllowHTTP bool   `json:"allowHTTP"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

func comCall(object unsafe.Pointer, index int, args ...uintptr) uintptr {
	table := *(*unsafe.Pointer)(object)
	fn := *(*uintptr)(unsafe.Add(table, index*int(unsafe.Sizeof(uintptr(0)))))
	call := append([]uintptr{uintptr(object)}, args...)
	result, _, _ := syscall.SyscallN(fn, call...)
	runtime.KeepAlive(object)
	return result
}
func comString(object unsafe.Pointer, index int) string {
	var text *uint16
	if int32(comCall(object, index, uintptr(unsafe.Pointer(&text)))) < 0 || text == nil {
		return ""
	}
	defer windows.CoTaskMemFree(unsafe.Pointer(text))
	return windows.UTF16PtrToString(text)
}
func (b *widgetBrowser) allowedDocument() bool {
	return b.core != nil && comString(b.core, 4) == "about:blank"
}

// Native COM event callbacks live as long as the browser and are not reclaimed
// until Close has disconnected them from the controller.
type webEventVtbl struct{ Query, AddRef, Release, Invoke uintptr }
type webEventHandler struct {
	Vtbl    *webEventVtbl
	Browser *widgetBrowser
	Kind    int
}

var eventQuery = syscall.NewCallback(func(this, iid, out uintptr) uintptr {
	if out == 0 {
		return 0x80004003
	}
	*(*unsafe.Pointer)(messagePointer(out)) = messagePointer(this)
	return 0
})
var eventAddRef = syscall.NewCallback(func(this uintptr) uintptr { return 1 })
var eventRelease = syscall.NewCallback(func(this uintptr) uintptr { return 1 })
var eventInvoke = syscall.NewCallback(func(this, sender, args uintptr) uintptr {
	h := (*webEventHandler)(messagePointer(this))
	if args == 0 {
		return 0
	}
	a := messagePointer(args)
	if h.Kind == 0 {
		if h.Browser.initialNavigation {
			h.Browser.initialNavigation = false
			return 0
		}
		// Only the first host-controlled NavigateToString is allowed. Even a
		// later about:blank reload would discard the authenticated local UI.
		comCall(a, 8, 1) // navigation cancel
	} else {
		comCall(a, 7, 1)
	} // new-window Handled=true
	return 0
})
var widgetEventVtbl = webEventVtbl{eventQuery, eventAddRef, eventRelease, eventInvoke}

func (b *widgetBrowser) guardNavigation() {
	for _, entry := range []struct{ slot, kind int }{{7, 0}, {17, 0}, {44, 1}} {
		h := &webEventHandler{Vtbl: &widgetEventVtbl, Browser: b, Kind: entry.kind}
		b.guards = append(b.guards, h)
		var token int64
		comCall(b.core, entry.slot, uintptr(unsafe.Pointer(h)), uintptr(unsafe.Pointer(&token)))
	}
}
func (w *nativeWindow) attachWidget() error {
	version, versionErr := webviewloader.GetInstalledVersion()
	if versionErr != nil || version == "" {
		return errors.New("Microsoft Edge WebView2 Runtime is required for the approved interface")
	}
	// Tests and callers may run on a different locked OS thread than package init.
	// WebView2 requires that exact thread to have a single-threaded COM apartment.
	hr, _, _ := windows.NewLazySystemDLL("ole32.dll").NewProc("CoInitializeEx").Call(0, 2)
	if int32(hr) < 0 {
		return errors.New("WebView2 requires a single-threaded COM apartment")
	}
	w.comInitialized = true
	chrome := edge.NewChromium()
	browser := &widgetBrowser{chromium: chrome, owner: w}
	// An isolated profile contains no Manager origins, cookies or admin key.
	profile := filepath.Join(w.store.dir, "renderer")
	if err := os.MkdirAll(profile, 0700); err != nil {
		return errors.New("cannot create renderer profile")
	}
	if err := privateACL(profile); err != nil {
		return errors.New("cannot protect renderer profile")
	}
	chrome.DataPath = profile
	chrome.SetGlobalPermission(edge.CoreWebView2PermissionStateDeny)
	chrome.MessageCallback = func(raw string) {
		if w.hwnd == 0 || !browser.allowedDocument() || len(raw) > 32768 {
			return
		}
		var m widgetMessage
		if json.Unmarshal([]byte(raw), &m) != nil {
			return
		}
		switch m.Action {
		case "ready", "measure", "pin", "compact", "close", "panel", "settings", "cancel", "drag", "connect":
		default:
			return
		}
		browser.pending = append(browser.pending, m)
		user32.NewProc("PostMessageW").Call(w.hwnd, wmWidgetAction, 0, 0)
	}
	showWindow.Call(w.hwnd, 0)
	// A previous native window may have posted WM_QUIT on this same UI thread.
	// It must not prematurely terminate WebView2's initialization message loop.
	var previous winMessage
	for {
		r, _, _ := peekMessage.Call(uintptr(unsafe.Pointer(&previous)), 0, 0x12, 0x12, 1)
		if r == 0 {
			break
		}
	}
	if !chrome.Embed(w.hwnd) {
		return errors.New("Microsoft Edge WebView2 Runtime is required for the approved interface")
	}
	controller := chrome.GetController()
	if controller == nil {
		return errors.New("WebView2 controller is unavailable")
	}
	var core unsafe.Pointer
	comCall(unsafe.Pointer(controller), 25, uintptr(unsafe.Pointer(&core)))
	if core == nil {
		return errors.New("WebView2 document is unavailable")
	}
	browser.core = core
	browser.guardNavigation()
	if settings, err := chrome.GetSettings(); err == nil {
		settings.PutAreDefaultContextMenusEnabled(false)
		settings.PutAreDevToolsEnabled(false)
		settings.PutAreHostObjectsAllowed(false)
		settings.PutIsZoomControlEnabled(false)
		settings.PutIsStatusBarEnabled(false)
		comCall(unsafe.Pointer(settings), 2)
	}
	if c := controller.GetICoreWebView2Controller2(); c != nil {
		c.PutDefaultBackgroundColor(edge.COREWEBVIEW2_COLOR{A: 255, R: 25, G: 29, B: 33})
		comCall(unsafe.Pointer(c), 2)
	}
	w.browser = browser
	if err := chrome.Show(); err != nil {
		return errors.New("cannot display WebView2 content")
	}
	chrome.NavigationCompletedCallback = func(_ *edge.ICoreWebView2, _ *edge.ICoreWebView2NavigationCompletedEventArgs) {
		if w.hwnd == 0 {
			return
		}
		browser.ready = true
		browser.last = ""
		w.updateWidget()
	}
	browser.initialNavigation = true
	chrome.NavigateToString(widgetDocument())
	w.layout()
	return nil
}
func (w *nativeWindow) updateWidget() {
	b := w.browser
	if b == nil || !b.ready || w.hwnd == 0 || !b.allowedDocument() {
		return
	}
	v := snapshotWidgetState(w.state)
	v.Lang = w.lang
	v.Compact = w.prefs.Compact
	v.Editing = w.editing
	v.Pinned = w.prefs.Topmost
	v.Base = w.prefs.BaseURL
	v.Remember = w.remember
	v.AllowHTTP = w.prefs.AllowPlainHTTP
	v.Notice = w.notice
	v.HasKey = w.client != nil
	script := widgetUpdateScript(v)
	if script == b.last {
		return
	}
	b.last = script
	b.chromium.Eval(script)
}
func (w *nativeWindow) handleWidgetMessages() {
	b := w.browser
	if b == nil {
		return
	}
	messages := b.pending
	b.pending = nil
	for _, m := range messages {
		switch m.Action {
		case "ready":
			b.ready = true
			b.last = ""
			w.updateWidget()
		case "measure":
			if m.Width == 354 && m.Height >= 90 && m.Height <= 900 {
				if !w.editing && !w.prefs.Compact {
					w.expandedHeight = m.Height
				}
				w.resizeAndClamp()
				b.chromium.Resize()
				if !b.shown {
					b.shown = true
					showWindow.Call(w.hwnd, 4)
					w.applyTopmost()
				}
			}
		case "drag":
			user32.NewProc("ReleaseCapture").Call()
			sendMessage.Call(w.hwnd, 0xA1, 2, 0)
		case "connect":
			key := keyForWidgetConnection(w.client, m.Base, m.Key, m.KeepKey, m.AllowHTTP)
			setText(w.controls[IDURL], m.Base)
			setText(w.controls[IDKey], key)
			check := uintptr(0)
			if m.Remember {
				check = 1
			}
			sendMessage.Call(w.controls[IDRemember], 0xF1, check, 0)
			check = 0
			if m.AllowHTTP {
				check = 1
			}
			sendMessage.Call(w.controls[IDAllowPlainHTTP], 0xF1, check, 0)
			w.connect()
			key = ""
			m.Key = ""
			w.makeFonts()
			w.updateWidget()
		default:
			id := map[string]int{"pin": IDPin, "compact": IDCompact, "close": IDClose, "panel": IDOpenPanel, "settings": IDConnection, "cancel": IDCancel}[m.Action]
			if id != 0 {
				sendMessage.Call(w.hwnd, wmCommand, uintptr(id), 0)
			}
		}
		if w.hwnd == 0 {
			return
		}
	}
}
func (w *nativeWindow) closeWidget() {
	defer func() {
		if w.comInitialized {
			windows.NewLazySystemDLL("ole32.dll").NewProc("CoUninitialize").Call()
			w.comInitialized = false
		}
	}()

	b := w.browser
	if b == nil {
		return
	}
	w.browser = nil
	b.ready = false
	b.pending = nil
	b.chromium.MessageCallback = nil
	b.chromium.NavigationCompletedCallback = nil
	if c := b.chromium.GetController(); c != nil {
		comCall(unsafe.Pointer(c), 24)
		// Balance GetCoreWebView2 + AddRef in the host binding and our own
		// GetCoreWebView2 reference before releasing the controller/environment.
		if b.core != nil {
			for i := 0; i < 3; i++ {
				comCall(b.core, 2)
			}
		}
		comCall(unsafe.Pointer(c), 2)
	}
	if environment := b.chromium.Environment(); environment != nil {
		comCall(unsafe.Pointer(environment), 2)
	}
	b.core = nil
	b.guards = nil
}
