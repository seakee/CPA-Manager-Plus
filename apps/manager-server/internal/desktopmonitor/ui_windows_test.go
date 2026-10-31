//go:build windows

package desktopmonitor

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func pumpUntil(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		var m winMessage
		for {
			ok, _, _ := peekMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, 1)
			if ok == 0 {
				break
			}
			translateMessage.Call(uintptr(unsafe.Pointer(&m)))
			dispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
		}
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("native condition timed out")
}
func controlText(h uintptr) string {
	n, _, _ := getWindowTextLength.Call(h)
	b := make([]uint16, n+1)
	getWindowText.Call(h, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)))
	return windows.UTF16ToString(b)
}
func clickControl(h uintptr, id int) {
	c, _, _ := getDlgItem.Call(h, uintptr(id))
	sendMessage.Call(c, 0xF5, 0, 0)
}
func TestNativeControlsLifecycleAndHTTP(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var auth atomic.Bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer native-fixture-key" {
			w.WriteHeader(401)
			return
		}
		if auth.Load() {
			w.WriteHeader(401)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "summary"):
			fmt.Fprint(w, `{"today":{"total_calls":7,"total_tokens":4321,"total_cost":0.3}}`)
		case strings.HasSuffix(r.URL.Path, "analytics"):
			fmt.Fprintf(w, `{"events":{"items":[{"timestamp_ms":%d,"model":"native-fixture","input_tokens":1000,"output_tokens":200,"cached_tokens":300,"total_tokens":1200,"latency_ms":2000,"ttft_ms":400,"stream":true}]}}`, time.Now().UnixMilli()-1000)
		default:
			fmt.Fprint(w, `{"prices":{"native-fixture":{"prompt":2,"completion":4,"cache":0.5}}}`)
		}
	}))
	defer s.Close()
	w, err := newNativeWindow(Options{BaseURL: s.URL, Lang: "en"}, preferencesStore{dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if w.hwnd != 0 {
			sendMessage.Call(w.hwnd, wmClose, 0, 0)
		}
	}()
	hwnd := w.hwnd
	pumpUntil(t, func() bool { return w.browser != nil && w.browser.ready && w.browser.shown })
	var browserVisible int32
	comCall(unsafe.Pointer(w.browser.chromium.GetController()), 3, uintptr(unsafe.Pointer(&browserVisible)))
	if browserVisible == 0 {
		t.Fatal("WebView2 is invisible inside the real desktop HWND")
	}
	for _, id := range []int{IDPin, IDCompact, IDHide, IDClose, IDConnection, IDURL, IDKey, IDRemember, IDConnect, IDCancel, IDStatus, IDModel, IDSpeed, IDTTFT, IDDuration, IDTokens, IDToday, IDRecent1, IDRecent2, IDUpdated} {
		h, _, _ := getDlgItem.Call(hwnd, uintptr(id))
		if h == 0 {
			t.Fatalf("missing control %d", id)
		}
	}
	for _, id := range []int{IDSpeedLabel, IDTTFTLabel, IDDurationLabel, IDTokenSection, IDTodaySection, IDRecentSection} {
		if text := controlText(w.controls[id]); text == "" {
			t.Fatalf("missing accessible section/metric label %d", id)
		}
	}
	if text := controlText(w.controls[IDRequest]); strings.Contains(text, "No recorded request") {
		t.Fatal("unloaded telemetry claims successful empty history")
	}
	if h, _, _ := getDlgItem.Call(hwnd, uintptr(IDOpenPanel)); h == 0 {
		t.Fatal("open panel control missing")
	}
	key := w.controls[IDKey]
	style, _, _ := getWindowLong.Call(key, uintptr(uint32(0xfffffff0)))
	if style&0x20 == 0 {
		t.Fatal("key not masked ES_PASSWORD")
	}
	setText(w.controls[IDKey], "native-fixture-key")
	clickControl(hwnd, IDConnect)
	pumpUntil(t, func() bool { return controlText(w.controls[IDModel]) == "native-fixture" })
	if got := controlText(w.controls[IDSpeed]); !strings.Contains(got, "100.0 tok/s") {
		t.Fatal(got)
	}
	if got := controlText(w.controls[IDTTFT]); !strings.Contains(got, "0.40 s") {
		t.Fatal(got)
	}
	style, _, _ = getWindowLong.Call(hwnd, uintptr(uint32(0xffffffec)))
	if style&8 == 0 {
		t.Fatal("not actually topmost")
	}
	clickControl(hwnd, IDPin)
	style, _, _ = getWindowLong.Call(hwnd, uintptr(uint32(0xffffffec)))
	if style&8 != 0 {
		t.Fatal("topmost did not toggle")
	}
	var before, after winRect
	getWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&before)))
	clickControl(hwnd, IDCompact)
	getWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&after)))
	if after.Bottom-after.Top >= before.Bottom-before.Top {
		t.Fatal("not compact")
	}
	if after.Bottom-after.Top > int32(w.scale(150)) || after.Right-after.Left > int32(w.scale(354)) {
		t.Fatal("compact is not a small desktop bar")
	}
	w.render()
	w.layout() // Must remain compact after the next polling refresh.
	if text := controlText(w.controls[IDTTFTLabel]); text != "TTFT" {
		t.Fatalf("compact label too verbose: %s", text)
	}
	if w.browser == nil || !w.browser.ready {
		t.Fatal("approved HTML renderer was not initialized")
	}
	// Native telemetry controls are kept only for the command/state boundary;
	// rendering is exclusively the original HTML/CSS, not overlapping GDI labels.
	for _, id := range []int{IDSpeed, IDTTFT, IDDuration, IDToday} {
		visible, _, _ := isWindowVisible.Call(w.controls[id])
		if visible != 0 {
			t.Fatalf("legacy control overlays HTML metric %d", id)
		}
	}
	for _, id := range []int{IDTokens, IDCost, IDRequest, IDRecent1, IDRecent2, IDTokenSection, IDRecentSection} {
		visible, _, _ := isWindowVisible.Call(w.controls[id])
		if visible != 0 {
			t.Fatalf("compact retained expanded content %d", id)
		}
	}
	clickControl(hwnd, IDHide)
	visible, _, _ := isWindowVisible.Call(hwnd)
	if visible != 0 {
		t.Fatal("not hidden")
	}
	// A documented tray menu command restores a hidden window independently of
	// notification overflow placement. The actual callback follows the same path.
	sendMessage.Call(hwnd, wmCommand, IDTrayRestore, 0)
	sendMessage.Call(hwnd, wmTray, 1, 0x202)
	visible, _, _ = isWindowVisible.Call(hwnd)
	if visible == 0 {
		t.Fatal("tray restore failed")
	}
	auth.Store(true)
	w.poller.Configure(w.client, w.state.Generation, true)
	pumpUntil(t, func() bool { return w.state.Status == StatusUnauthorized })
	if controlText(w.controls[IDModel]) != "—" || strings.Contains(controlText(w.controls[IDToday]), "4,321") {
		t.Fatal("auth retained telemetry")
	}
	sendMessage.Call(hwnd, wmClose, 0, 0)
	exists, _, _ := isWindow.Call(hwnd)
	if exists != 0 {
		t.Fatal("window not destroyed")
	}
	select {
	case <-w.poller.done:
	case <-time.After(time.Second):
		t.Fatal("close did not join poller")
	}
	// Close exits the companion process. Reopening is verified through a fresh
	// executable instance in the desktop smoke test, not by reusing a quit thread.
}
