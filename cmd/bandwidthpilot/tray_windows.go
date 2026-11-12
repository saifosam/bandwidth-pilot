//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"

	"bandwidthpilot/internal/core"
)

// The notification-area (tray) icon. It lives on a hidden message-only
// window on the UI thread, so its messages are pumped by the webview's
// message loop. The main window is subclassed so that its close button
// hides it instead of quitting while "close to tray" is on.

var (
	shell32              = windows.NewLazySystemDLL("shell32.dll")
	procShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")

	procRegisterClassExW         = user32.NewProc("RegisterClassExW")
	procCreateWindowExW          = user32.NewProc("CreateWindowExW")
	procDefWindowProcW           = user32.NewProc("DefWindowProcW")
	procCallWindowProcW          = user32.NewProc("CallWindowProcW")
	procSetWindowLongPtrW        = user32.NewProc("SetWindowLongPtrW")
	procShowWindow               = user32.NewProc("ShowWindow")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procIsIconic                 = user32.NewProc("IsIconic")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procCreatePopupMenu          = user32.NewProc("CreatePopupMenu")
	procAppendMenuW              = user32.NewProc("AppendMenuW")
	procTrackPopupMenu           = user32.NewProc("TrackPopupMenu")
	procDestroyMenu              = user32.NewProc("DestroyMenu")
	procGetCursorPos             = user32.NewProc("GetCursorPos")
	procPostMessageW             = user32.NewProc("PostMessageW")
	procLoadImageW               = user32.NewProc("LoadImageW")
	procSetTimer                 = user32.NewProc("SetTimer")
	procRegisterWindowMessageW   = user32.NewProc("RegisterWindowMessageW")
	procFindWindowW              = user32.NewProc("FindWindowW")
	procGetModuleHandleW         = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW")
	procAllowSetForegroundWindow = user32.NewProc("AllowSetForegroundWindow")
)

const (
	wmClose       = 0x0010
	wmCommand     = 0x0111
	wmTimer       = 0x0113
	wmNull        = 0x0000
	wmLButtonUp   = 0x0202
	wmRButtonUp   = 0x0205
	wmLButtonDbl  = 0x0203
	wmApp         = 0x8000
	wmTray        = wmApp + 1
	ninBalloonClk = 0x0405

	nimAdd    = 0
	nimModify = 1
	nimDelete = 2

	nifMessage = 0x01
	nifIcon    = 0x02
	nifTip     = 0x04
	nifInfo    = 0x10
	niifInfo   = 0x01
	niifUser   = 0x04

	swHide    = 0
	swShow    = 5
	swRestore = 9

	mfString    = 0x0000
	mfChecked   = 0x0008
	mfSeparator = 0x0800
	mfGrayed    = 0x0001

	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100
	tpmBottomAlign = 0x0020

	gwlpWndProc = ^uintptr(3) // -4
	hwndMessage = ^uintptr(2) // -3

	cmdOpen    = 1
	cmdStartup = 2
	cmdPause   = 3
	cmdQuit    = 4
)

type notifyIconData struct {
	CbSize           uint32
	HWnd             windows.HWND
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            windows.Handle
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         windows.GUID
	HBalloonIcon     windows.Handle
}

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     windows.Handle
	HIcon         windows.Handle
	HCursor       windows.Handle
	HbrBackground windows.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       windows.Handle
}

type shellPrefs struct {
	CloseToTray bool `json:"closeToTray"`
	HintShown   bool `json:"hintShown"`
}

type tray struct {
	sv       *core.Service
	w        webview2.WebView
	main     windows.HWND
	msg      windows.HWND
	icon     windows.Handle
	oldProc  uintptr
	quitting atomic.Bool
	taskbar  uint32 // "TaskbarCreated": Explorer restarted, re-add the icon
	prefPath string
	prefs    shellPrefs
}

var theTray *tray // the callbacks below are plain functions

func newTray(sv *core.Service, w webview2.WebView, main windows.HWND, dir string) *tray {
	t := &tray{sv: sv, w: w, main: main, prefPath: filepath.Join(dir, "window.json"), prefs: shellPrefs{CloseToTray: true}}
	if b, err := os.ReadFile(t.prefPath); err == nil {
		json.Unmarshal(b, &t.prefs)
	}
	theTray = t

	hinst, _, _ := procGetModuleHandleW.Call(0)
	t.icon = windows.Handle(loadIcon(hinst, 16))
	cls, _ := windows.UTF16PtrFromString("BandwidthPilotTray")
	wc := wndClassEx{LpfnWndProc: windows.NewCallback(trayProc), HInstance: windows.Handle(hinst), LpszClassName: cls}
	wc.CbSize = uint32(unsafe.Sizeof(wc))
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	h, _, _ := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), 0, 0, 0, 0, 0, 0, hwndMessage, 0, hinst, 0)
	t.msg = windows.HWND(h)
	tb, _ := windows.UTF16PtrFromString("TaskbarCreated")
	r, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(tb)))
	t.taskbar = uint32(r)

	t.add()
	procSetTimer.Call(uintptr(t.msg), 1, 2000, 0)
	t.oldProc, _, _ = procSetWindowLongPtrW.Call(uintptr(main), gwlpWndProc, windows.NewCallback(mainProc))

	sv.OnPending = func(name string) {
		w.Dispatch(func() {
			t.balloon("New app wants to connect",
				name+" is waiting. Open Bandwidth Pilot to allow or block it.")
		})
	}
	sv.Hooks["shellPrefs"] = t.rpcPrefs
	return t
}

func loadIcon(hinst uintptr, size int) uintptr {
	h, _, _ := procLoadImageW.Call(hinst, 1, 1 /* IMAGE_ICON */, uintptr(size), uintptr(size), 0)
	return h
}

func (t *tray) data(flags uint32) *notifyIconData {
	d := &notifyIconData{HWnd: t.msg, UID: 1, UFlags: flags, UCallbackMessage: wmTray, HIcon: t.icon}
	d.CbSize = uint32(unsafe.Sizeof(*d))
	copy(d.SzTip[:127], windows.StringToUTF16("Bandwidth Pilot"))
	return d
}

func (t *tray) add() {
	procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(t.data(nifMessage|nifIcon|nifTip))))
}

func (t *tray) remove() {
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(t.data(0))))
}

func (t *tray) balloon(title, text string) {
	d := t.data(nifInfo)
	copy(d.SzInfoTitle[:63], windows.StringToUTF16(title))
	copy(d.SzInfo[:255], windows.StringToUTF16(text))
	d.DwInfoFlags = niifInfo
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(d)))
}

func (t *tray) updateTip() {
	snap := t.sv.Snapshot()
	tip := fmt.Sprintf("Bandwidth Pilot\n↓ %s   ↑ %s", rateText(snap.RateIn), rateText(snap.RateOut))
	if n := len(snap.Pending); n > 0 {
		tip += fmt.Sprintf("\n%d app(s) waiting for approval", n)
	}
	d := t.data(nifTip)
	copy(d.SzTip[:127], windows.StringToUTF16(tip))
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(d)))
}

func rateText(b int64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.2f MB/s", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.0f KB/s", float64(b)/(1<<10))
	}
	return fmt.Sprintf("%d B/s", b)
}

func (t *tray) show() {
	if r, _, _ := procIsIconic.Call(uintptr(t.main)); r != 0 {
		showWindow(t.main, swRestore)
	} else {
		showWindow(t.main, swShow)
	}
	procSetForegroundWindow.Call(uintptr(t.main))
}

func (t *tray) quit() {
	t.quitting.Store(true)
	procPostMessageW.Call(uintptr(t.main), wmClose, 0, 0)
}

func (t *tray) menu() {
	m, _, _ := procCreatePopupMenu.Call()
	defer procDestroyMenu.Call(m)
	item := func(id uintptr, flags uintptr, text string) {
		p, _ := windows.UTF16PtrFromString(text)
		procAppendMenuW.Call(m, flags, id, uintptr(unsafe.Pointer(p)))
	}
	cfg := t.sv.S.Config()
	paused := !cfg.Blocker && !cfg.Limiter && !cfg.Priorities
	item(cmdOpen, mfString, "Open Bandwidth Pilot")
	procAppendMenuW.Call(m, mfSeparator, 0, 0)
	pauseFlags := uintptr(mfString)
	if paused {
		pauseFlags |= mfChecked
	}
	item(cmdPause, pauseFlags, "Pause all rules")
	startFlags := uintptr(mfString)
	if startupEnabled() {
		startFlags |= mfChecked
	}
	item(cmdStartup, startFlags, "Start with Windows")
	procAppendMenuW.Call(m, mfSeparator, 0, 0)
	item(cmdQuit, mfString, "Quit (removes all limits)")

	var pt struct{ X, Y int32 }
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(uintptr(t.msg))
	cmd, _, _ := procTrackPopupMenu.Call(m, tpmRightButton|tpmReturnCmd|tpmBottomAlign,
		uintptr(pt.X), uintptr(pt.Y), 0, uintptr(t.msg), 0)
	procPostMessageW.Call(uintptr(t.msg), wmNull, 0, 0)
	switch cmd {
	case cmdOpen:
		t.show()
	case cmdPause:
		on := paused // paused -> resume everything
		cfg.Blocker, cfg.Limiter, cfg.Priorities = on, on, on
		raw, _ := json.Marshal(map[string]any{"config": cfg})
		t.sv.Call("setConfig", raw)
	case cmdStartup:
		if err := setStartup(!startupEnabled()); err != nil {
			messageBox("Could not change the startup setting:\n\n" + err.Error())
		}
	case cmdQuit:
		t.quit()
	}
}

func (t *tray) savePrefs() {
	if b, err := json.Marshal(t.prefs); err == nil {
		os.WriteFile(t.prefPath, b, 0o644)
	}
}

// rpcPrefs lets the Settings page read and change the window options.
func (t *tray) rpcPrefs(raw json.RawMessage) (any, error) {
	var p struct {
		CloseToTray *bool `json:"closeToTray"`
		Startup     *bool `json:"startup"`
	}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
	}
	if p.CloseToTray != nil {
		t.prefs.CloseToTray = *p.CloseToTray
		t.savePrefs()
	}
	if p.Startup != nil && *p.Startup != startupEnabled() {
		if err := setStartup(*p.Startup); err != nil {
			return nil, err
		}
	}
	return map[string]bool{"closeToTray": t.prefs.CloseToTray, "startup": startupEnabled(), "available": true}, nil
}

func trayProc(hwnd, msg, wp, lp uintptr) uintptr {
	t := theTray
	if t != nil {
		switch {
		case msg == wmTray:
			switch lp & 0xFFFF {
			case wmLButtonUp, wmLButtonDbl, ninBalloonClk:
				t.show()
			case wmRButtonUp:
				t.menu()
			}
			return 0
		case msg == wmTimer:
			t.updateTip()
			return 0
		case t.taskbar != 0 && uint32(msg) == t.taskbar:
			t.add()
			return 0
		}
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

func mainProc(hwnd, msg, wp, lp uintptr) uintptr {
	t := theTray
	if t != nil && msg == wmClose && t.prefs.CloseToTray && !t.quitting.Load() {
		showWindow(t.main, swHide)
		if !t.prefs.HintShown {
			t.prefs.HintShown = true
			t.savePrefs()
			t.balloon("Bandwidth Pilot is still running",
				"Your limits stay on. Right-click this icon and choose Quit to stop it.")
		}
		return 0
	}
	r, _, _ := procCallWindowProcW.Call(t.oldProc, hwnd, msg, wp, lp)
	return r
}

func showWindow(h windows.HWND, cmd uintptr) {
	procShowWindow.Call(uintptr(h), cmd)
}

// showExisting brings an already running instance to the front.
func showExisting() bool {
	cls, _ := windows.UTF16PtrFromString("webview")
	title, _ := windows.UTF16PtrFromString("Bandwidth Pilot")
	h, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)))
	if h == 0 {
		return false
	}
	procAllowSetForegroundWindow.Call(^uintptr(0)) // ASFW_ANY
	if r, _, _ := procIsIconic.Call(h); r != 0 {
		procShowWindow.Call(h, swRestore)
	} else {
		procShowWindow.Call(h, swShow)
	}
	procSetForegroundWindow.Call(h)
	return true
}
