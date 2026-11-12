//go:build windows

package engine

import (
	"path/filepath"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Finding which processes are "apps" the way Task Manager does: a process
// is an app when it owns a visible, unowned, titled top-level window that is
// not a tool window and not cloaked (hidden UWP/virtual-desktop windows).

var (
	procGetWindow             = user32.NewProc("GetWindow")
	procGetWindowLongPtrW     = user32.NewProc("GetWindowLongPtrW")
	procGetWindowTextLengthW  = user32.NewProc("GetWindowTextLengthW")
	dwmapi                    = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmGetWindowAttribute = dwmapi.NewProc("DwmGetWindowAttribute")
)

const (
	gwOwner         = 4
	gwlExStyle      = ^uintptr(19) // -20
	wsExToolWindow  = 0x00000080
	dwmwaCloaked    = 14
	frameHostClass  = "ApplicationFrameWindow"
	coreWindowClass = "Windows.UI.Core.CoreWindow"
)

func className(h windows.HWND) string {
	buf := make([]uint16, 128)
	n, err := windows.GetClassName(h, &buf[0], int32(len(buf)))
	if err != nil || n <= 0 {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

func isCloaked(h windows.HWND) bool {
	if procDwmGetWindowAttribute.Find() != nil {
		return false
	}
	var v uint32
	r, _, _ := procDwmGetWindowAttribute.Call(uintptr(h), dwmwaCloaked, uintptr(unsafe.Pointer(&v)), 4)
	return r == 0 && v != 0
}

var (
	enumMu   sync.Mutex
	enumPIDs map[uint32]bool

	topCallback = windows.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		h := windows.HWND(hwnd)
		if !windows.IsWindowVisible(h) || isCloaked(h) {
			return 1
		}
		if owner, _, _ := procGetWindow.Call(hwnd, gwOwner); owner != 0 {
			return 1
		}
		if ex, _, _ := procGetWindowLongPtrW.Call(hwnd, gwlExStyle); ex&wsExToolWindow != 0 {
			return 1
		}
		if n, _, _ := procGetWindowTextLengthW.Call(hwnd); n == 0 {
			return 1
		}
		var pid uint32
		windows.GetWindowThreadProcessId(h, &pid)
		if pid != 0 {
			enumPIDs[pid] = true
		}
		// Store apps draw inside a frame window owned by ApplicationFrameHost;
		// the real app process owns the CoreWindow child.
		if className(h) == frameHostClass {
			windows.EnumChildWindows(h, childCallback, nil)
		}
		return 1
	})

	childCallback = windows.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		h := windows.HWND(hwnd)
		if className(h) == coreWindowClass {
			var pid uint32
			windows.GetWindowThreadProcessId(h, &pid)
			if pid != 0 {
				enumPIDs[pid] = true
			}
		}
		return 1
	})
)

// windowedPIDs returns the ids of processes that show an app window.
func windowedPIDs() map[uint32]bool {
	enumMu.Lock()
	defer enumMu.Unlock()
	enumPIDs = map[uint32]bool{}
	windows.EnumWindows(topCallback, nil)
	out := enumPIDs
	enumPIDs = nil
	return out
}

// isFrameHost reports whether a path is the Store-app frame host, which owns
// windows on behalf of other processes and should not itself count as an app.
func isFrameHost(path string) bool {
	return strings.EqualFold(filepath.Base(path), "ApplicationFrameHost.exe")
}
