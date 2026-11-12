//go:build windows

package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Thin bindings for WinDivert 2.2 (https://reqrypt.org/windivert.html).
// WinDivert.dll and WinDivert64.sys must sit next to the executable.

const (
	wdLayerNetwork = 0
	wdLayerFlow    = 2

	wdFlagSniff    = 0x0001
	wdFlagRecvOnly = 0x0004

	wdShutdownRecv = 0x1

	wdParamQueueLength = 0
	wdParamQueueTime   = 1
	wdParamQueueSize   = 2

	wdEventFlowEstablished = 1
	wdEventFlowDeleted     = 2

	wdAddrSize = 80
	wdMTUMax   = 40 + 0xFFFF

	// Bits of the flags word at offset 8 of WINDIVERT_ADDRESS.
	wdAddrOutbound = 1 << 17
)

var (
	wdDLL          *windows.LazyDLL
	wdProcOpen     *windows.LazyProc
	wdProcRecv     *windows.LazyProc
	wdProcSend     *windows.LazyProc
	wdProcShutdown *windows.LazyProc
	wdProcClose    *windows.LazyProc
	wdProcSetParam *windows.LazyProc
)

func wdLoad() error {
	if wdDLL != nil {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dir := filepath.Dir(exe)
	dll := filepath.Join(dir, "WinDivert.dll")
	for _, f := range []string{dll, filepath.Join(dir, "WinDivert64.sys")} {
		if _, err := os.Stat(f); err != nil {
			return fmt.Errorf("%s is missing. Keep WinDivert.dll and WinDivert64.sys in the same folder as BandwidthPilot.exe", filepath.Base(f))
		}
	}
	d := windows.NewLazyDLL(dll)
	if err := d.Load(); err != nil {
		return fmt.Errorf("could not load WinDivert.dll: %w", err)
	}
	wdProcOpen = d.NewProc("WinDivertOpen")
	wdProcRecv = d.NewProc("WinDivertRecv")
	wdProcSend = d.NewProc("WinDivertSend")
	wdProcShutdown = d.NewProc("WinDivertShutdown")
	wdProcClose = d.NewProc("WinDivertClose")
	wdProcSetParam = d.NewProc("WinDivertSetParam")
	for _, p := range []*windows.LazyProc{wdProcOpen, wdProcRecv, wdProcSend, wdProcShutdown, wdProcClose, wdProcSetParam} {
		if err := p.Find(); err != nil {
			return fmt.Errorf("WinDivert.dll is not the expected version (2.2): %w", err)
		}
	}
	wdDLL = d
	return nil
}

func wdOpen(filter string, layer int, priority int16, flags uint64) (uintptr, error) {
	f, err := windows.BytePtrFromString(filter)
	if err != nil {
		return 0, err
	}
	h, _, e := wdProcOpen.Call(uintptr(unsafe.Pointer(f)), uintptr(layer), uintptr(uint16(priority)), uintptr(flags))
	if h == 0 || h == ^uintptr(0) {
		return 0, wdExplain(e)
	}
	return h, nil
}

// wdRecv blocks until a packet (or event) arrives.
func wdRecv(h uintptr, buf []byte, addr *[wdAddrSize]byte) (int, syscall.Errno) {
	var n uint32
	r, _, e := wdProcRecv.Call(h, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)),
		uintptr(unsafe.Pointer(&n)), uintptr(unsafe.Pointer(addr)))
	if r == 0 {
		if en, ok := e.(syscall.Errno); ok && en != 0 {
			return 0, en
		}
		return 0, syscall.Errno(windows.ERROR_GEN_FAILURE)
	}
	return int(n), 0
}

func wdSend(h uintptr, pkt []byte, addr *[wdAddrSize]byte) bool {
	if len(pkt) == 0 {
		return false
	}
	r, _, _ := wdProcSend.Call(h, uintptr(unsafe.Pointer(&pkt[0])), uintptr(len(pkt)),
		0, uintptr(unsafe.Pointer(addr)))
	return r != 0
}

func wdShutdown(h uintptr, how uintptr) { wdProcShutdown.Call(h, how) }
func wdClose(h uintptr)                 { wdProcClose.Call(h) }

func wdSetParam(h uintptr, param, value uint64) {
	wdProcSetParam.Call(h, uintptr(param), uintptr(value))
}

// wdExplain turns WinDivertOpen's error codes into something a person can act on.
func wdExplain(e error) error {
	en, ok := e.(syscall.Errno)
	if !ok {
		return e
	}
	switch en {
	case windows.ERROR_ACCESS_DENIED:
		return errors.New("access denied. Bandwidth Pilot must run as administrator")
	case windows.ERROR_FILE_NOT_FOUND:
		return errors.New("the WinDivert driver file (WinDivert64.sys) could not be found next to BandwidthPilot.exe")
	case windows.ERROR_INVALID_IMAGE_HASH:
		return errors.New("Windows rejected the WinDivert driver's signature")
	case windows.ERROR_DRIVER_BLOCKED:
		return errors.New("the WinDivert driver was blocked, usually by antivirus or a security policy. Allow WinDivert64.sys and try again")
	case windows.ERROR_DRIVER_FAILED_PRIOR_UNLOAD:
		return errors.New("an older WinDivert driver is still loaded by another program. Close it or restart Windows")
	case windows.ERROR_SERVICE_DOES_NOT_EXIST:
		return errors.New("the WinDivert driver is not installed and could not be installed")
	case windows.EPT_S_NOT_REGISTERED:
		return errors.New("the Base Filtering Engine service is not running; Windows needs it for packet filtering")
	}
	return fmt.Errorf("WinDivert error %d: %s", uint32(en), en.Error())
}
