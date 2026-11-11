//go:build windows

// cpa-manager-plus-monitor is an independent read-only Windows desktop client.
package main

import (
	"flag"
	"os"
	"unsafe"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/desktopmonitor"
	"golang.org/x/sys/windows"
)

func main() {
	url := flag.String("url", "", "Manager URL (loopback HTTP or remote HTTPS; no key)")
	lang := flag.String("lang", "en", "UI language: en or zh")
	flag.Parse()
	if flag.NArg() != 0 {
		os.Exit(2)
	}
	if err := desktopmonitor.RunWithOptions(desktopmonitor.Options{BaseURL: *url, Lang: *lang}); err != nil {
		// Only fixed/sanitized errors from the monitor are shown. No secret flag exists.
		title, _ := windows.UTF16PtrFromString("CPAMP Monitor")
		text, _ := windows.UTF16PtrFromString(err.Error())
		windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x10)
		os.Exit(1)
	}
}
