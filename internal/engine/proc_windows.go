//go:build windows

package engine

import (
	"encoding/binary"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"bandwidthpilot/internal/shaper"
)

// ---------------------------------------------------------------------------
// Port -> process id tables (iphlpapi). Used for connections that existed
// before Bandwidth Pilot started and for packets that race ahead of flow events.
// ---------------------------------------------------------------------------

var (
	iphlpapi             = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTcpTb = iphlpapi.NewProc("GetExtendedTcpTable")
	procGetExtendedUdpTb = iphlpapi.NewProc("GetExtendedUdpTable")
)

const (
	afInet            = 2
	afInet6           = 23
	tcpTableOwnerPID  = 5 // TCP_TABLE_OWNER_PID_ALL
	udpTableOwnerPID  = 1 // UDP_TABLE_OWNER_PID
	portRefreshMinGap = 150 * time.Millisecond
)

type portTable struct {
	mu   sync.Mutex
	tcp  map[uint32]uint32 // lport<<16 | rport -> pid
	tcpL map[uint16]uint32 // lport -> pid (listeners and fallback)
	udp  map[uint16]uint32 // lport -> pid
	last time.Time
	buf  []byte
}

func newPortTable() *portTable {
	return &portTable{tcp: map[uint32]uint32{}, tcpL: map[uint16]uint32{}, udp: map[uint16]uint32{}}
}

func (t *portTable) find(proto uint8, lport, rport uint16) (uint32, bool) {
	if proto == protoTCP {
		if pid, ok := t.tcp[uint32(lport)<<16|uint32(rport)]; ok {
			return pid, true
		}
		pid, ok := t.tcpL[lport]
		return pid, ok
	}
	pid, ok := t.udp[lport]
	return pid, ok
}

// lookup returns the owning process id, refreshing the tables on a miss.
func (t *portTable) lookup(proto uint8, lport, rport uint16) (uint32, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if pid, ok := t.find(proto, lport, rport); ok {
		return pid, true
	}
	if time.Since(t.last) < portRefreshMinGap {
		return 0, false
	}
	t.refresh()
	return t.find(proto, lport, rport)
}

func (t *portTable) query(proc *windows.LazyProc, af, class uintptr) []byte {
	size := uint32(len(t.buf))
	for i := 0; i < 4; i++ {
		var p unsafe.Pointer
		if len(t.buf) > 0 {
			p = unsafe.Pointer(&t.buf[0])
		}
		r, _, _ := proc.Call(uintptr(p), uintptr(unsafe.Pointer(&size)), 0, af, class, 0)
		if r == 0 {
			return t.buf[:size]
		}
		if syscall.Errno(r) != windows.ERROR_INSUFFICIENT_BUFFER {
			return nil
		}
		t.buf = make([]byte, size+4096)
		size = uint32(len(t.buf))
	}
	return nil
}

// netPort reads a port stored in network byte order in the low half of a DWORD.
func netPort(b []byte) uint16 { return uint16(b[0])<<8 | uint16(b[1]) }

func (t *portTable) refresh() {
	t.last = time.Now()
	tcp := make(map[uint32]uint32, len(t.tcp))
	tcpL := make(map[uint16]uint32, len(t.tcpL))
	udp := make(map[uint16]uint32, len(t.udp))
	le := binary.LittleEndian

	// MIB_TCPROW_OWNER_PID: state, localAddr, localPort, remoteAddr, remotePort, pid (24 bytes)
	if b := t.query(procGetExtendedTcpTb, afInet, tcpTableOwnerPID); len(b) >= 4 {
		n := int(le.Uint32(b))
		for i, off := 0, 4; i < n && off+24 <= len(b); i, off = i+1, off+24 {
			lp, rp, pid := netPort(b[off+8:]), netPort(b[off+16:]), le.Uint32(b[off+20:])
			tcp[uint32(lp)<<16|uint32(rp)] = pid
			tcpL[lp] = pid
		}
	}
	// MIB_TCP6ROW_OWNER_PID: localAddr[16], scope, localPort, remoteAddr[16], scope, remotePort, state, pid (56 bytes)
	if b := t.query(procGetExtendedTcpTb, afInet6, tcpTableOwnerPID); len(b) >= 4 {
		n := int(le.Uint32(b))
		for i, off := 0, 4; i < n && off+56 <= len(b); i, off = i+1, off+56 {
			lp, rp, pid := netPort(b[off+20:]), netPort(b[off+44:]), le.Uint32(b[off+52:])
			tcp[uint32(lp)<<16|uint32(rp)] = pid
			tcpL[lp] = pid
		}
	}
	// MIB_UDPROW_OWNER_PID: localAddr, localPort, pid (12 bytes)
	if b := t.query(procGetExtendedUdpTb, afInet, udpTableOwnerPID); len(b) >= 4 {
		n := int(le.Uint32(b))
		for i, off := 0, 4; i < n && off+12 <= len(b); i, off = i+1, off+12 {
			udp[netPort(b[off+4:])] = le.Uint32(b[off+8:])
		}
	}
	// MIB_UDP6ROW_OWNER_PID: localAddr[16], scope, localPort, pid (28 bytes)
	if b := t.query(procGetExtendedUdpTb, afInet6, udpTableOwnerPID); len(b) >= 4 {
		n := int(le.Uint32(b))
		for i, off := 0, 4; i < n && off+28 <= len(b); i, off = i+1, off+28 {
			udp[netPort(b[off+20:])] = le.Uint32(b[off+24:])
		}
	}
	t.tcp, t.tcpL, t.udp = tcp, tcpL, udp
}

// ---------------------------------------------------------------------------
// Process id -> app. Processes are grouped by executable path.
// ---------------------------------------------------------------------------

type procEntry struct {
	app *shaper.App
	at  time.Time
}

type procCache struct {
	s     *shaper.Shaper
	mu    sync.Mutex
	pids  map[uint32]procEntry
	names map[string]string // exe path -> friendly name
}

func newProcCache(s *shaper.Shaper) *procCache {
	return &procCache{s: s, pids: map[uint32]procEntry{}, names: map[string]string{}}
}

const (
	appIDUnknown = "unknown"
	appIDSystem  = "system"
)

func (c *procCache) unknown() *shaper.App {
	return c.s.App(appIDUnknown, "Unidentified traffic", "")
}

// app maps a process id to its app. Results are cached briefly because
// process ids get reused.
func (c *procCache) app(pid uint32) *shaper.App {
	switch pid {
	case 0:
		return c.unknown()
	case 4:
		return c.s.App(appIDSystem, "System", "")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.pids[pid]; ok && time.Since(e.at) < 20*time.Second {
		return e.app
	}
	var a *shaper.App
	if path := processPath(pid); path != "" {
		name, ok := c.names[path]
		if !ok {
			name = fileDescription(path)
			if name == "" {
				name = filepath.Base(path)
			}
			c.names[path] = name
		}
		a = c.s.App(strings.ToLower(path), name, path)
	} else if exe := processExeName(pid); exe != "" {
		a = c.s.App("proc:"+strings.ToLower(exe), exe, "")
	} else {
		a = c.unknown()
	}
	if len(c.pids) > 4096 {
		c.pids = map[uint32]procEntry{}
	}
	c.pids[pid] = procEntry{app: a, at: time.Now()}
	return a
}

func processPath(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, 1024)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

// processExeName is the fallback for processes that cannot be opened.
func processExeName(pid uint32) string {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(snap)
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		if pe.ProcessID == pid {
			return windows.UTF16ToString(pe.ExeFile[:])
		}
	}
	return ""
}

// fileDescription reads the "File description" from an executable's version
// resource, which is the friendly name Task Manager shows.
func fileDescription(path string) string {
	var zero windows.Handle
	size, err := windows.GetFileVersionInfoSize(path, &zero)
	if err != nil || size == 0 {
		return ""
	}
	data := make([]byte, size)
	if err := windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&data[0])); err != nil {
		return ""
	}
	// Find the language/codepage pairs, then ask for FileDescription in each.
	type langCP struct{ lang, cp uint16 }
	tries := []langCP{}
	var tp unsafe.Pointer
	var tl uint32
	if err := windows.VerQueryValue(unsafe.Pointer(&data[0]), `\VarFileInfo\Translation`, unsafe.Pointer(&tp), &tl); err == nil && tp != nil {
		pairs := unsafe.Slice((*langCP)(tp), int(tl)/4)
		tries = append(tries, pairs...)
	}
	tries = append(tries, langCP{0x0409, 0x04B0}, langCP{0x0409, 0x04E4}, langCP{0, 0x04B0})
	const hex = "0123456789abcdef"
	for _, t := range tries {
		key := []byte(`\StringFileInfo\`)
		for _, v := range []uint16{t.lang, t.cp} {
			key = append(key, hex[v>>12&15], hex[v>>8&15], hex[v>>4&15], hex[v&15])
		}
		key = append(key, `\FileDescription`...)
		var vp unsafe.Pointer
		var vl uint32
		if err := windows.VerQueryValue(unsafe.Pointer(&data[0]), string(key), unsafe.Pointer(&vp), &vl); err == nil && vp != nil && vl > 1 {
			s := strings.TrimSpace(windows.UTF16ToString(unsafe.Slice((*uint16)(vp), int(vl))))
			if s != "" {
				return s
			}
		}
	}
	return ""
}
