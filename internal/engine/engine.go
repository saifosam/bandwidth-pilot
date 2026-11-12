// Package engine connects the shaper to real (or simulated) network traffic.
//
// An Engine captures packets, works out which app each one belongs to, asks
// the shaper what to do with it, and re-injects it when allowed. The Windows
// engine does this with WinDivert; the demo engine simulates traffic so the
// app runs on any OS. A macOS engine only has to implement this interface.
package engine

import (
	"fmt"
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"bandwidthpilot/internal/shaper"
	"bandwidthpilot/internal/sites"
)

// Conn is one connection as shown in the UI.
type Conn struct {
	Proto      string `json:"proto"`
	LocalPort  uint16 `json:"localPort"`
	Remote     string `json:"remote"`
	Host       string `json:"host"`
	RemotePort uint16 `json:"remotePort"`
	PID        uint32 `json:"pid"`
	RateIn     int64  `json:"rateIn"`
	RateOut    int64  `json:"rateOut"`
	In         int64  `json:"in"`
	Out        int64  `json:"out"`
}

// Status describes what the engine is doing.
type Status struct {
	Mode         string `json:"mode"`   // "live" or "demo"
	Driver       string `json:"driver"` // e.g. "WinDivert 2.2"
	Running      bool   `json:"running"`
	Intercepting bool   `json:"intercepting"`
	Error        string `json:"error"`
}

// Engine is a packet source bound to a shaper.
type Engine interface {
	Start() error
	Stop()
	Status() Status
	// Release re-injects a packet the shaper was holding.
	Release(p *shaper.Packet)
	// Housekeep runs once a second: connection rates, expiry.
	Housekeep()
	// Connections lists an app's live connections.
	Connections(appID string) []Conn
	// Summary returns connection counts and process ids per app id.
	Summary() map[string]AppProcs
	// Icon returns a PNG data URL for the app's icon, or "".
	Icon(appID string) string
	// Windowed returns the ids of apps that currently show a window.
	Windowed() map[string]bool
	// Resplit reattributes traffic after an app is split into (or merged
	// back from) one row per process.
	Resplit()
}

// ConnEvent is one entry of the connection log.
type ConnEvent struct {
	Time     int64  `json:"time"` // unix milliseconds
	AppID    string `json:"appId"`
	App      string `json:"app"`
	PID      uint32 `json:"pid"`
	Proto    string `json:"proto"`
	Remote   string `json:"remote"`
	Host     string `json:"host"`
	Port     uint16 `json:"port"`
	Outbound bool   `json:"outbound"`
	Blocked  bool   `json:"blocked"`
	Site     string `json:"site"`
}

// Options connects an engine to the rest of the app.
type Options struct {
	Logf Logf
	// Sites matches remote addresses to website rules; Match must return
	// the rule's *shaper.App.
	Sites *sites.Table
	// Split reports whether an app is shown as one row per process.
	Split func(appID string) bool
	// OnConn receives the first packet of every new connection.
	OnConn func(ConnEvent)
}

func (o *Options) logf(format string, args ...any) {
	if o != nil && o.Logf != nil {
		o.Logf(format, args...)
	}
}

// splitApp returns the per-process row for pid when its app is split.
func (o *Options) splitApp(s *shaper.Shaper, a *shaper.App, pid uint32) *shaper.App {
	if o == nil || o.Split == nil || pid == 0 || a.Parent != "" || !o.Split(a.ID) {
		return a
	}
	return s.AppWithParent(fmt.Sprintf("pid:%d:%s", pid, a.ID), fmt.Sprintf("%s (PID %d)", a.Name, pid), a.Path, a.ID)
}

// route returns the website rule app for a flow, or nil, caching the
// answer until the rules or the DNS name change.
func (o *Options) route(f *flow, remote netip.Addr) *shaper.App {
	if o == nil || o.Sites == nil {
		return nil
	}
	gen := o.Sites.Gen()
	if f.viaGen.Load() == gen {
		return f.via.Load()
	}
	host := o.Sites.Host(remote)
	f.host.Store(&host)
	var via *shaper.App
	if m, ok := o.Sites.Match(remote).(*shaper.App); ok {
		via = m
	}
	f.via.Store(via)
	f.viaGen.Store(gen)
	return via
}

// admit asks the shaper about one packet, through a website rule if any.
func admit(s *shaper.Shaper, app, via *shaper.App, d shaper.Dir, size int) shaper.Verdict {
	if via != nil {
		return s.AdmitVia(app, via, d, size)
	}
	return s.Admit(app, d, size)
}

// logConn reports the first packet of a flow to the connection log.
func (o *Options) logConn(f *flow, app, via *shaper.App, remote netip.Addr, outbound, blocked bool) {
	if o == nil || o.OnConn == nil || f.logged.Swap(true) {
		return
	}
	ev := ConnEvent{Time: time.Now().UnixMilli(), AppID: app.ID, App: app.Name, PID: f.pid.Load(),
		Proto: "TCP", Port: f.key.rport, Outbound: outbound, Blocked: blocked}
	if f.key.proto == protoUDP {
		ev.Proto = "UDP"
	}
	if remote.IsValid() {
		ev.Remote = remote.String()
	}
	if h := f.host.Load(); h != nil {
		ev.Host = *h
	}
	if via != nil {
		ev.Site = via.Name
	}
	o.OnConn(ev)
}

// AppProcs summarises an app's processes and connections.
type AppProcs struct {
	Conns int
	PIDs  []uint32
}

// Logf is the logging hook engines write diagnostics to.
type Logf func(format string, args ...any)

const (
	protoTCP = 6
	protoUDP = 17
)

type flowKey struct {
	proto uint8
	lport uint16
	rport uint16
}

// flow is one tracked connection.
type flow struct {
	key    flowKey
	remote atomic.Pointer[netip.Addr]
	bytes  [2]atomic.Int64
	last   atomic.Int64 // unix ms of the last packet
	closed atomic.Bool

	app      atomic.Pointer[shaper.App]
	host     atomic.Pointer[string]
	via      atomic.Pointer[shaper.App]
	viaGen   atomic.Int64
	logged   atomic.Bool
	born     atomic.Int64 // unix ms of the first packet
	pid      atomic.Uint32
	resolved atomic.Bool
	retryAt  atomic.Int64

	prev [2]int64 // housekeeping only
	rate [2]int64
}

type flowTable struct {
	mu sync.RWMutex
	m  map[flowKey]*flow
}

func newFlowTable() *flowTable { return &flowTable{m: map[flowKey]*flow{}} }

func (t *flowTable) get(k flowKey) *flow {
	t.mu.RLock()
	f := t.m[k]
	t.mu.RUnlock()
	return f
}

// put stores f unless a flow with that key already exists; it returns the
// flow that ends up in the table.
func (t *flowTable) put(f *flow) *flow {
	t.mu.Lock()
	defer t.mu.Unlock()
	if old := t.m[f.key]; old != nil {
		return old
	}
	t.m[f.key] = f
	return f
}

// housekeep computes per-connection rates and drops dead or idle flows.
func (t *flowTable) housekeep(now time.Time) {
	ms := now.UnixMilli()
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, f := range t.m {
		idle := ms - f.last.Load()
		limit := int64(120_000)
		if k.proto == protoUDP {
			limit = 30_000
		}
		if f.closed.Load() {
			limit = 3_000
		}
		if idle > limit {
			delete(t.m, k)
			continue
		}
		for d := 0; d < 2; d++ {
			cur := f.bytes[d].Load()
			f.rate[d] = cur - f.prev[d]
			f.prev[d] = cur
		}
	}
}

func (t *flowTable) connections(appID string) []Conn {
	t.mu.RLock()
	var out []Conn
	for k, f := range t.m {
		a := f.app.Load()
		if a == nil || a.ID != appID {
			continue
		}
		proto := "TCP"
		if k.proto == protoUDP {
			proto = "UDP"
		}
		remote, host := "", ""
		if r := f.remote.Load(); r != nil {
			remote = r.String()
		}
		if h := f.host.Load(); h != nil {
			host = *h
		}
		out = append(out, Conn{
			Proto: proto, LocalPort: k.lport, Remote: remote, Host: host, RemotePort: k.rport,
			PID: f.pid.Load(), RateIn: f.rate[0], RateOut: f.rate[1],
			In: f.bytes[0].Load(), Out: f.bytes[1].Load(),
		})
	}
	t.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		ri, rj := out[i].RateIn+out[i].RateOut, out[j].RateIn+out[j].RateOut
		if ri != rj {
			return ri > rj
		}
		ti, tj := out[i].In+out[i].Out, out[j].In+out[j].Out
		if ti != tj {
			return ti > tj
		}
		if out[i].Remote != out[j].Remote {
			return out[i].Remote < out[j].Remote
		}
		return out[i].LocalPort < out[j].LocalPort
	})
	if len(out) > 200 {
		out = out[:200]
	}
	return out
}

func (t *flowTable) summary() map[string]AppProcs {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := map[string]AppProcs{}
	seen := map[string]map[uint32]bool{}
	for _, f := range t.m {
		a := f.app.Load()
		if a == nil {
			continue
		}
		p := out[a.ID]
		p.Conns++
		if pid := f.pid.Load(); pid != 0 {
			if seen[a.ID] == nil {
				seen[a.ID] = map[uint32]bool{}
			}
			if !seen[a.ID][pid] {
				seen[a.ID][pid] = true
				p.PIDs = append(p.PIDs, pid)
			}
		}
		out[a.ID] = p
	}
	for id, p := range out {
		sort.Slice(p.PIDs, func(i, j int) bool { return p.PIDs[i] < p.PIDs[j] })
		out[id] = p
	}
	return out
}

// failedEngine stands in when the real engine could not start, so the UI
// can show why.
type failedEngine struct{ msg string }

// NewFailed returns a placeholder engine that only reports an error.
func NewFailed(msg string) Engine { return &failedEngine{msg: msg} }

func (e *failedEngine) Start() error                 { return nil }
func (e *failedEngine) Stop()                        {}
func (e *failedEngine) Status() Status               { return Status{Mode: "live", Error: e.msg} }
func (e *failedEngine) Release(*shaper.Packet)       {}
func (e *failedEngine) Housekeep()                   {}
func (e *failedEngine) Connections(string) []Conn    { return nil }
func (e *failedEngine) Summary() map[string]AppProcs { return nil }
func (e *failedEngine) Icon(string) string           { return "" }
func (e *failedEngine) Resplit()                     {}
func (e *failedEngine) Windowed() map[string]bool    { return nil }

// each calls fn for every tracked flow.
func (t *flowTable) each(fn func(*flow)) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, f := range t.m {
		fn(f)
	}
}
