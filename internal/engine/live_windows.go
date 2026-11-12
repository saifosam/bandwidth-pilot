//go:build windows

package engine

import (
	"encoding/binary"
	"fmt"
	"io"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/windows"

	"bandwidthpilot/internal/shaper"
)

// The Windows engine.
//
// Two WinDivert handles are open while it runs:
//
//   - a FLOW-layer handle that reports every new connection together with
//     the id of the process that owns it. That is how packets are matched to apps.
//   - a NETWORK-layer handle that sees every TCP/UDP packet.
//
// The network handle has two modes. While no rule needs enforcing it only
// sniffs: Windows delivers packets normally and Bandwidth Pilot just counts copies,
// so it cannot slow anything down. As soon as a limit, block, quota or cap is
// active it switches to intercepting: packets are diverted to Bandwidth Pilot, which
// passes, delays or drops each one and re-injects the survivors.

const netFilter = "!loopback and (tcp or udp)"

// held is what a queued packet needs to be re-injected later.
type held struct {
	h    uintptr
	addr [wdAddrSize]byte
}

type liveEngine struct {
	s     *shaper.Shaper
	logf  Logf
	flows *flowTable
	ports *portTable
	procs *procCache
	icons *iconCache

	running atomic.Bool
	want    atomic.Bool // should the network handle intercept?

	netMu        sync.Mutex
	netH         uintptr
	netIntercept bool
	flowH        atomic.Uintptr

	wg      sync.WaitGroup
	errMu   sync.Mutex
	lastErr string

	winMu    sync.Mutex
	windowed map[string]bool
	winTick  int
}

// NewLive returns the WinDivert engine.
func NewLive(s *shaper.Shaper, logf Logf) (Engine, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if err := wdLoad(); err != nil {
		return nil, err
	}
	e := &liveEngine{s: s, logf: logf, flows: newFlowTable(), ports: newPortTable(),
		procs: newProcCache(s), icons: newIconCache()}
	return e, nil
}

var (
	winmm               = windows.NewLazySystemDLL("winmm.dll")
	procTimeBeginPeriod = winmm.NewProc("timeBeginPeriod")
	procTimeEndPeriod   = winmm.NewProc("timeEndPeriod")
)

func (e *liveEngine) openNet(intercept bool) (uintptr, error) {
	var flags uint64
	if !intercept {
		flags = wdFlagSniff | wdFlagRecvOnly
	}
	h, err := wdOpen(netFilter, wdLayerNetwork, 0, flags)
	if err != nil {
		return 0, err
	}
	wdSetParam(h, wdParamQueueLength, 16384)
	wdSetParam(h, wdParamQueueSize, 33554432)
	wdSetParam(h, wdParamQueueTime, 2000)
	return h, nil
}

func (e *liveEngine) Start() error {
	if e.running.Load() {
		return nil
	}
	e.want.Store(e.s.NeedsIntercept())
	// Open the first network handle here so that driver problems surface as
	// a startup error instead of a silent background failure.
	first := e.want.Load()
	h, err := e.openNet(first)
	if err != nil {
		e.setErr(err.Error())
		return err
	}
	e.setErr("")
	e.running.Store(true)
	procTimeBeginPeriod.Call(1)
	e.ports.mu.Lock()
	e.ports.refresh()
	e.ports.mu.Unlock()
	e.wg.Add(2)
	go e.flowLoop()
	go e.netLoop(h, first)
	e.logf("engine started (intercept=%v)", first)
	return nil
}

func (e *liveEngine) Stop() {
	if !e.running.Swap(false) {
		return
	}
	e.netMu.Lock()
	if e.netH != 0 {
		wdShutdown(e.netH, wdShutdownRecv)
	}
	e.netMu.Unlock()
	if h := e.flowH.Load(); h != 0 {
		wdShutdown(h, wdShutdownRecv)
	}
	done := make(chan struct{})
	go func() { e.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		e.logf("engine stop timed out")
	}
	procTimeEndPeriod.Call(1)
	e.logf("engine stopped")
}

func (e *liveEngine) setErr(s string) {
	e.errMu.Lock()
	e.lastErr = s
	e.errMu.Unlock()
}

// SetIntercept is called by the shaper whenever rules start or stop needing
// packets to be held. It retires the current network handle; netLoop reopens
// it in the right mode.
func (e *liveEngine) SetIntercept(on bool) {
	e.want.Store(on)
	e.netMu.Lock()
	if e.netH != 0 && e.netIntercept != on {
		wdShutdown(e.netH, wdShutdownRecv)
	}
	e.netMu.Unlock()
}

func (e *liveEngine) netLoop(h uintptr, intercept bool) {
	defer e.wg.Done()
	buf := make([]byte, wdMTUMax)
	var addr [wdAddrSize]byte
	for {
		e.netMu.Lock()
		e.netH, e.netIntercept = h, intercept
		e.netMu.Unlock()
		// The wanted mode may have changed while the handle was opening.
		if e.want.Load() != intercept || !e.running.Load() {
			wdShutdown(h, wdShutdownRecv)
		}

		e.pump(h, intercept, buf, &addr)

		// The handle is drained. Let go of anything still queued, then close.
		if intercept {
			e.s.FlushAll()
		}
		e.netMu.Lock()
		e.netH = 0
		e.netMu.Unlock()
		wdClose(h)
		if !e.running.Load() {
			return
		}

		for {
			intercept = e.want.Load()
			var err error
			if h, err = e.openNet(intercept); err == nil {
				e.setErr("")
				e.logf("network handle reopened (intercept=%v)", intercept)
				break
			}
			e.setErr(err.Error())
			e.logf("reopen failed: %v", err)
			time.Sleep(2 * time.Second)
			if !e.running.Load() {
				return
			}
		}
	}
}

// pump reads packets from h until the handle is shut down and drained.
func (e *liveEngine) pump(h uintptr, intercept bool, buf []byte, addr *[wdAddrSize]byte) {
	failures := 0
	for {
		n, errno := wdRecv(h, buf, addr)
		if errno != 0 {
			if errno == windows.ERROR_NO_DATA || errno == windows.ERROR_INVALID_HANDLE ||
				errno == windows.ERROR_OPERATION_ABORTED || !e.running.Load() {
				return
			}
			if failures++; failures > 200 {
				e.logf("recv keeps failing: %v", errno)
				return
			}
			continue
		}
		failures = 0
		e.handle(h, intercept, buf[:n], addr)
	}
}

func (e *liveEngine) handle(h uintptr, intercept bool, pkt []byte, addr *[wdAddrSize]byte) {
	info, ok := parsePacket(pkt)
	if !ok {
		if intercept {
			wdSend(h, pkt, addr)
		}
		return
	}
	outbound := binary.LittleEndian.Uint32(addr[8:12])&wdAddrOutbound != 0
	dir := shaper.In
	k := flowKey{proto: info.proto, lport: info.dport, rport: info.sport}
	remote := info.src
	if outbound {
		dir = shaper.Out
		k.lport, k.rport = info.sport, info.dport
		remote = info.dst
	}
	ms := time.Now().UnixMilli()
	f := e.flowFor(k, ms)
	if f.remote.Load() == nil {
		f.remote.Store(&remote)
	}
	f.last.Store(ms)
	app := f.app.Load()
	size := len(pkt)

	if !intercept {
		e.s.Count(app, dir, size)
		f.bytes[dir].Add(int64(size))
		return
	}
	switch e.s.Admit(app, dir, size) {
	case shaper.Pass:
		wdSend(h, pkt, addr)
		f.bytes[dir].Add(int64(size))
	case shaper.Queue:
		data := make([]byte, size)
		copy(data, pkt)
		e.s.Enqueue(&shaper.Packet{App: app, Dir: dir, Size: size, Data: data, Meta: &held{h: h, addr: *addr}})
		f.bytes[dir].Add(int64(size))
	case shaper.Drop:
	}
}

// Release re-injects a packet the shaper was holding.
func (e *liveEngine) Release(p *shaper.Packet) {
	if m, ok := p.Meta.(*held); ok {
		wdSend(m.h, p.Data, &m.addr)
	}
}

func (e *liveEngine) flowFor(k flowKey, ms int64) *flow {
	if f := e.flows.get(k); f != nil {
		if !f.resolved.Load() && ms >= f.retryAt.Load() {
			e.resolve(f, ms)
		}
		return f
	}
	f := &flow{key: k}
	e.resolve(f, ms)
	return e.flows.put(f)
}

// resolve finds the owning process of a flow through the port tables.
func (e *liveEngine) resolve(f *flow, ms int64) {
	if pid, ok := e.ports.lookup(f.key.proto, f.key.lport, f.key.rport); ok && pid != 0 {
		f.pid.Store(pid)
		f.app.Store(e.procs.app(pid))
		f.resolved.Store(true)
		return
	}
	if f.app.Load() == nil {
		f.app.Store(e.procs.unknown())
	}
	f.retryAt.Store(ms + 500)
}

// flowLoop listens for connection events, which carry the owning process id.
func (e *liveEngine) flowLoop() {
	defer e.wg.Done()
	h, err := wdOpen("true", wdLayerFlow, 0, wdFlagSniff|wdFlagRecvOnly)
	if err != nil {
		// Not fatal: the port tables still attribute most traffic.
		e.logf("flow layer unavailable: %v", err)
		return
	}
	e.flowH.Store(h)
	defer func() {
		e.flowH.Store(0)
		wdClose(h)
	}()
	if !e.running.Load() {
		return
	}
	buf := make([]byte, 64)
	var addr [wdAddrSize]byte
	le := binary.LittleEndian
	failures := 0
	for {
		_, errno := wdRecv(h, buf, &addr)
		if errno != 0 {
			if errno == windows.ERROR_NO_DATA || errno == windows.ERROR_INVALID_HANDLE ||
				errno == windows.ERROR_OPERATION_ABORTED || !e.running.Load() {
				return
			}
			if errno == windows.ERROR_INSUFFICIENT_BUFFER {
				continue
			}
			if failures++; failures > 200 {
				e.logf("flow recv keeps failing: %v", syscall.Errno(errno))
				return
			}
			continue
		}
		failures = 0
		// WINDIVERT_ADDRESS: event at byte 9; the flow data starts at byte 16:
		// endpoint ids (16 bytes), process id, local addr[16], remote addr[16],
		// local port, remote port, protocol.
		event := addr[9]
		pid := le.Uint32(addr[32:])
		k := flowKey{proto: addr[72], lport: le.Uint16(addr[68:]), rport: le.Uint16(addr[70:])}
		if k.proto != protoTCP && k.proto != protoUDP {
			continue
		}
		switch event {
		case wdEventFlowEstablished:
			f := e.flows.get(k)
			if f == nil {
				f = &flow{key: k}
				f.last.Store(time.Now().UnixMilli())
				f = e.flows.put(f)
			}
			f.pid.Store(pid)
			f.app.Store(e.procs.app(pid))
			f.resolved.Store(true)
			f.closed.Store(false)
		case wdEventFlowDeleted:
			if f := e.flows.get(k); f != nil {
				f.closed.Store(true)
			}
		}
	}
}

func (e *liveEngine) Status() Status {
	e.netMu.Lock()
	open, intercept := e.netH != 0, e.netIntercept
	e.netMu.Unlock()
	e.errMu.Lock()
	msg := e.lastErr
	e.errMu.Unlock()
	return Status{Mode: "live", Driver: "WinDivert 2.2", Running: e.running.Load() && (open || msg == ""),
		Intercepting: open && intercept, Error: msg}
}

func (e *liveEngine) Housekeep() {
	e.flows.housekeep(time.Now())
	// Re-scan the desktop's windows every other second.
	if e.winTick++; e.winTick%2 == 1 {
		set := map[string]bool{}
		for pid := range windowedPIDs() {
			if a := e.procs.app(pid); a != nil && a.Path != "" && !isFrameHost(a.Path) {
				set[a.ID] = true
			}
		}
		e.winMu.Lock()
		e.windowed = set
		e.winMu.Unlock()
	}
}

func (e *liveEngine) Windowed() map[string]bool {
	e.winMu.Lock()
	defer e.winMu.Unlock()
	return e.windowed
}

func (e *liveEngine) Connections(appID string) []Conn { return e.flows.connections(appID) }
func (e *liveEngine) Summary() map[string]AppProcs    { return e.flows.summary() }

func (e *liveEngine) Icon(appID string) string {
	if a := e.s.Lookup(appID); a != nil {
		return e.icons.get(a.Path)
	}
	return ""
}

// SelfTest exercises the Windows API plumbing that does not need the driver
// and prints what it finds. Run with: BandwidthPilot.exe --selftest
func SelfTest(w io.Writer) {
	fmt.Fprintf(w, "Bandwidth Pilot self-test (pid %d)\n", os.Getpid())

	pt := newPortTable()
	pt.refresh()
	fmt.Fprintf(w, "port tables: %d tcp connections, %d tcp ports, %d udp ports\n", len(pt.tcp), len(pt.tcpL), len(pt.udp))

	s := shaper.New(func(*shaper.Packet) {})
	pc := newProcCache(s)
	seen := map[uint32]bool{}
	for _, m := range []map[uint16]uint32{pt.tcpL, pt.udp} {
		for port, pid := range m {
			if seen[pid] || len(seen) >= 12 {
				continue
			}
			seen[pid] = true
			a := pc.app(pid)
			fmt.Fprintf(w, "  port %-5d pid %-6d -> %q (%s)\n", port, pid, a.Name, a.Path)
		}
	}

	fmt.Fprintf(w, "processes with an app window: %d\n", len(windowedPIDs()))

	self, _ := os.Executable()
	a := pc.app(uint32(os.Getpid()))
	fmt.Fprintf(w, "own process: name=%q path=%q\n", a.Name, a.Path)
	for _, p := range []string{self, `C:\Windows\explorer.exe`, `C:\Windows\System32\notepad.exe`} {
		png := extractIconPNG(p)
		fmt.Fprintf(w, "icon %s: %d bytes of PNG, description=%q\n", p, len(png), fileDescription(p))
	}

	b := make([]byte, 40)
	b[0], b[9] = 0x45, 6
	copy(b[12:], netip.MustParseAddr("10.0.0.2").AsSlice())
	if _, ok := parsePacket(b); !ok {
		fmt.Fprintln(w, "packet parser: FAILED")
	} else {
		fmt.Fprintln(w, "packet parser: ok")
	}

	if err := wdLoad(); err != nil {
		fmt.Fprintf(w, "WinDivert: %v\n", err)
		return
	}
	fmt.Fprintln(w, "WinDivert.dll: loaded")
	h, err := wdOpen(netFilter, wdLayerNetwork, 0, wdFlagSniff|wdFlagRecvOnly)
	if err != nil {
		fmt.Fprintf(w, "WinDivert driver: %v\n", err)
		return
	}
	fmt.Fprintln(w, "WinDivert driver: opened a sniffing handle")
	done := make(chan int, 1)
	go func() {
		buf := make([]byte, wdMTUMax)
		var addr [wdAddrSize]byte
		n := 0
		for {
			if _, errno := wdRecv(h, buf, &addr); errno != 0 {
				break
			}
			n++
		}
		done <- n
	}()
	time.Sleep(2 * time.Second)
	wdShutdown(h, wdShutdownRecv)
	fmt.Fprintf(w, "WinDivert driver: saw %d packets in 2 seconds\n", <-done)
	wdClose(h)
}
