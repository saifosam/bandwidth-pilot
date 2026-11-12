//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"

	"bandwidthpilot/internal/core"
	"bandwidthpilot/internal/ui"
)

// runShell opens the native window: a Win32 window hosting the system
// WebView2 control, with the backend bound in as window.pilotCall.
func runShell(sv *core.Service, o options) error {
	if o.serve {
		return serveHTTP(sv, o)
	}
	runtime.LockOSThread()

	// One instance only: two would fight over the same packets. A second
	// launch brings the running window to the front instead.
	name, _ := windows.UTF16PtrFromString("Local\\BandwidthPilotSingleInstance")
	if _, err := windows.CreateMutex(nil, false, name); err == windows.ERROR_ALREADY_EXISTS {
		if !showExisting() {
			messageBox("Bandwidth Pilot is already running.")
		}
		return nil
	}

	// Size the window in logical pixels so it is not tiny on scaled displays.
	width, height := windowSize(1320, 820)

	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     o.debug,
		AutoFocus: true,
		DataPath:  filepath.Join(cache, "Bandwidth Pilot", "WebView2"),
		WindowOptions: webview2.WindowOptions{
			Title:  "Bandwidth Pilot",
			Width:  width,
			Height: height,
			IconId: 1,
			Center: true,
		},
	})
	if w == nil {
		messageBox("Bandwidth Pilot needs the Microsoft Edge WebView2 Runtime, which could not be started.\n\n" +
			"It is part of Windows 11. On Windows 10, install it from Microsoft, then start Bandwidth Pilot again.")
		return nil
	}
	defer w.Destroy()

	hwnd := windows.HWND(uintptr(w.Window()))
	if o.tray {
		showWindow(hwnd, swHide)
	}
	styleWindow(hwnd)
	minW, minH := windowSize(980, 600)
	w.SetSize(int(minW), int(minH), webview2.HintMin)
	if err := w.Bind("pilotCall", func(method string, params json.RawMessage) (interface{}, error) {
		return sv.Call(method, params)
	}); err != nil {
		return err
	}
	t := newTray(sv, w, hwnd, o.dir)
	defer t.remove()
	w.SetHtml(ui.HTML(""))
	w.Run()
	return nil
}

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	procGetDpiForSystem  = user32.NewProc("GetDpiForSystem")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")
)

// windowSize scales a size in logical pixels by the display scale and keeps
// it inside the primary screen.
func windowSize(w, h uint) (uint, uint) {
	dpi := uintptr(96)
	if procGetDpiForSystem.Find() == nil {
		if d, _, _ := procGetDpiForSystem.Call(); d >= 96 && d <= 960 {
			dpi = d
		}
	}
	w, h = w*uint(dpi)/96, h*uint(dpi)/96
	sw, _, _ := procGetSystemMetrics.Call(0) // SM_CXSCREEN
	sh, _, _ := procGetSystemMetrics.Call(1) // SM_CYSCREEN
	if sw > 200 && w > uint(sw)-40 {
		w = uint(sw) - 40
	}
	if sh > 200 && h > uint(sh)-80 {
		h = uint(sh) - 80
	}
	return w, h
}

// styleWindow gives the title bar the app's dark colour.
func styleWindow(hwnd windows.HWND) {
	const (
		dwmwaUseImmersiveDarkMode = 20
		dwmwaCaptionColor         = 35
	)
	dark := uint32(1)
	windows.DwmSetWindowAttribute(hwnd, dwmwaUseImmersiveDarkMode, unsafe.Pointer(&dark), 4)
	caption := uint32(0x00201F1F) // COLORREF is 0x00BBGGRR
	windows.DwmSetWindowAttribute(hwnd, dwmwaCaptionColor, unsafe.Pointer(&caption), 4)
}

func messageBox(text string) {
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString("Bandwidth Pilot")
	windows.MessageBox(0, t, c, windows.MB_OK|windows.MB_ICONINFORMATION)
}
