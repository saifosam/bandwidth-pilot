// Package shaper is the platform-independent heart of Bandwidth Pilot.
//
// A packet engine (WinDivert on Windows, a simulator elsewhere) hands every
// packet to Admit, which answers with one of three verdicts:
//
//	Pass  - let it through right now
//	Drop  - discard it (blocked, over quota, or the queue is full)
//	Queue - hold it; the engine wraps it in a Packet and calls Enqueue
//
// Queued packets are released by Tick, which the engine calls every couple of
// milliseconds. Tick spends tokens from two kinds of token bucket:
//
//   - a per-app bucket for "limit this app to N bytes/s" rules
//   - one global bucket per direction for the total-bandwidth cap
//
// When the global bucket is the bottleneck, apps are served in priority order
// (Critical > High > Normal > Low), round-robin inside a priority class.
// That is what makes "prioritise this app" mean something.
package shaper

import (
	"sync"
	"sync/atomic"
	"time"
)

// Dir is a traffic direction, seen from this computer.
type Dir int

const (
	In  Dir = 0 // download
	Out Dir = 1 // upload
)

// Verdict is the shaper's decision for one packet.
type Verdict int

const (
	Pass Verdict = iota
	Drop
	Queue
)

// Priority classes.
const (
	PrioLow      = 0
	PrioNormal   = 1
	PrioHigh     = 2
	PrioCritical = 3
)

// Limit is a speed limit in bytes per second.
type Limit struct {
	On  bool  `json:"on"`
	Bps int64 `json:"bps"`
}

// Quota is a daily data allowance (download + upload) in bytes.
type Quota struct {
	On    bool  `json:"on"`
	Bytes int64 `json:"bytes"`
}

// Rule is everything the user can configure for one app.
type Rule struct {
	BlockIn  bool  `json:"blockIn"`
	BlockOut bool  `json:"blockOut"`
	LimitIn  Limit `json:"limitIn"`
	LimitOut Limit `json:"limitOut"`
	Priority int   `json:"priority"`
	Quota    Quota `json:"quota"`
}

// DefaultRule is the rule an app has when the user has not touched it.
func DefaultRule() Rule { return Rule{Priority: PrioNormal} }

// IsDefault reports whether the rule does nothing.
func (r *Rule) IsDefault() bool {
	return r == nil || (!r.BlockIn && !r.BlockOut && !r.LimitIn.On && !r.LimitOut.On &&
		r.Priority == PrioNormal && !r.Quota.On)
}

func (r *Rule) limit(d Dir) Limit {
	if d == In {
		return r.LimitIn
	}
	return r.LimitOut
}

func (r *Rule) block(d Dir) bool {
	if d == In {
		return r.BlockIn
	}
	return r.BlockOut
}

// Config holds the master switches and the total-bandwidth cap.
type Config struct {
	Blocker    bool  `json:"blocker"`
	Limiter    bool  `json:"limiter"`
	Priorities bool  `json:"priorities"`
	CapOn      bool  `json:"capOn"`
	CapIn      int64 `json:"capIn"`  // bytes per second
	CapOut     int64 `json:"capOut"` // bytes per second
}

func (c *Config) capRate(d Dir) float64 {
	if !c.Limiter || !c.CapOn {
		return 0
	}
	if d == In {
		return float64(c.CapIn)
	}
	return float64(c.CapOut)
}

// Packet is one held packet. Data and Meta are opaque to the shaper; the
// engine uses them to re-inject the packet when it is released.
type Packet struct {
	App  *App
	Dir  Dir
	Size int
	Data []byte
	Meta any

	at time.Time
}

// App is one application (all processes sharing an executable).
type App struct {
	ID   string
	Name string
	Path string

	passed  [2]atomic.Int64 // bytes let through, per direction
	dropped [2]atomic.Int64 // bytes discarded, per direction
	used    atomic.Int64    // bytes counted against today's quota
	rule    atomic.Pointer[Rule]

	// Everything below is guarded by Shaper.mu.
	q       [2]fifo
	qBytes  [2]int64
	tokens  [2]float64
	refill  [2]time.Time
	backlog [2]bool
	served  [2]time.Time // when this app last got a packet out of its queue
}

// Passed returns the bytes let through in direction d since start.
func (a *App) Passed(d Dir) int64 { return a.passed[d].Load() }

// Dropped returns the bytes discarded in direction d since start.
func (a *App) Dropped(d Dir) int64 { return a.dropped[d].Load() }

// Used returns the bytes counted against today's quota.
func (a *App) Used() int64 { return a.used.Load() }

// SetUsed overwrites today's quota usage (loaded from disk, or reset at midnight).
func (a *App) SetUsed(v int64) { a.used.Store(v) }

// Rule returns a copy of the app's rule.
func (a *App) Rule() Rule {
	if r := a.rule.Load(); r != nil {
		return *r
	}
	return DefaultRule()
}

// QuotaExceeded reports whether the app is currently cut off by its quota.
func (a *App) QuotaExceeded() bool {
	r := a.rule.Load()
	return r != nil && r.Quota.On && r.Quota.Bytes > 0 && a.used.Load() >= r.Quota.Bytes
}

// Shaper decides the fate of every packet.
type Shaper struct {
	cfg atomic.Pointer[Config]

	appsMu sync.RWMutex
	apps   map[string]*App

	mu      sync.Mutex
	gTokens [2]float64
	gRefill [2]time.Time
	backlog [2][]*App
	rr      [2]int
	scratch []*App

	release     func(*Packet)
	now         func() time.Time
	intercept   atomic.Bool
	onIntercept func(bool)
}

const (
	// Queues hold at most this many seconds of traffic at the limited rate.
	queueSeconds = 0.6
	// A queued packet older than this is discarded.
	maxQueueAge = 3 * time.Second
	// A backlogged app is never starved for longer than this.
	starvationGuard = 250 * time.Millisecond
)

// New creates a shaper. release is called (from Tick or FlushAll) for every
// queued packet that may now be sent.
func New(release func(*Packet)) *Shaper {
	s := &Shaper{apps: map[string]*App{}, release: release, now: time.Now}
	s.cfg.Store(&Config{Blocker: true, Limiter: true, Priorities: true})
	return s
}

// SetRelease replaces the release callback.
func (s *Shaper) SetRelease(f func(*Packet)) { s.release = f }

// SetClock replaces the time source (tests).
func (s *Shaper) SetClock(now func() time.Time) { s.now = now }

// OnIntercept registers a callback fired whenever the answer to "does any
// rule need packets to be held or dropped?" changes. Engines use it to switch
// between a passive sniffing mode and an intercepting mode.
func (s *Shaper) OnIntercept(f func(bool)) { s.onIntercept = f }

// NeedsIntercept reports whether any active rule needs packets intercepted.
func (s *Shaper) NeedsIntercept() bool { return s.intercept.Load() }

// Config returns a copy of the current configuration.
func (s *Shaper) Config() Config { return *s.cfg.Load() }

// SetConfig replaces the configuration.
func (s *Shaper) SetConfig(c Config) {
	if c.CapIn < 0 {
		c.CapIn = 0
	}
	if c.CapOut < 0 {
		c.CapOut = 0
	}
	s.cfg.Store(&c)
	s.recompute()
}

// App returns the app with the given id, creating it if needed.
func (s *Shaper) App(id, name, path string) *App {
	s.appsMu.RLock()
	a := s.apps[id]
	s.appsMu.RUnlock()
	if a != nil {
		return a
	}
	s.appsMu.Lock()
	defer s.appsMu.Unlock()
	if a = s.apps[id]; a != nil {
		return a
	}
	a = &App{ID: id, Name: name, Path: path}
	s.apps[id] = a
	return a
}

// Lookup returns the app with the given id, or nil.
func (s *Shaper) Lookup(id string) *App {
	s.appsMu.RLock()
	defer s.appsMu.RUnlock()
	return s.apps[id]
}

// Apps returns a snapshot of all known apps.
func (s *Shaper) Apps() []*App {
	s.appsMu.RLock()
	defer s.appsMu.RUnlock()
	out := make([]*App, 0, len(s.apps))
	for _, a := range s.apps {
		out = append(out, a)
	}
	return out
}

// SetRule installs a rule for an app.
func (s *Shaper) SetRule(a *App, r Rule) {
	if r.Priority < PrioLow || r.Priority > PrioCritical {
		r.Priority = PrioNormal
	}
	if r.LimitIn.Bps < 0 {
		r.LimitIn.Bps = 0
	}
	if r.LimitOut.Bps < 0 {
		r.LimitOut.Bps = 0
	}
	if r.Quota.Bytes < 0 {
		r.Quota.Bytes = 0
	}
	a.rule.Store(&r)
	s.recompute()
}

// ClearRule removes an app's rule.
func (s *Shaper) ClearRule(a *App) {
	a.rule.Store(nil)
	s.recompute()
}

func (s *Shaper) recompute() {
	cfg := s.cfg.Load()
	need := cfg.capRate(In) > 0 || cfg.capRate(Out) > 0
	if !need {
		for _, a := range s.Apps() {
			r := a.rule.Load()
			if r == nil {
				continue
			}
			if (cfg.Blocker && (r.BlockIn || r.BlockOut)) ||
				(cfg.Limiter && ((r.LimitIn.On && r.LimitIn.Bps > 0) || (r.LimitOut.On && r.LimitOut.Bps > 0))) ||
				(r.Quota.On && r.Quota.Bytes > 0) {
				need = true
				break
			}
		}
	}
	if s.intercept.Swap(need) != need && s.onIntercept != nil {
		s.onIntercept(need)
	}
}

func appRate(cfg *Config, r *Rule, d Dir) float64 {
	if r == nil || !cfg.Limiter {
		return 0
	}
	if l := r.limit(d); l.On && l.Bps > 0 {
		return float64(l.Bps)
	}
	return 0
}

func prioOf(cfg *Config, a *App) int {
	if !cfg.Priorities {
		return PrioNormal
	}
	if r := a.rule.Load(); r != nil {
		return r.Priority
	}
	return PrioNormal
}

func burst(rate float64) float64 {
	b := rate * 0.05
	if b < 3000 {
		b = 3000
	}
	return b
}

func (s *Shaper) refillApp(a *App, d Dir, rate float64, now time.Time) {
	b := burst(rate)
	if a.refill[d].IsZero() {
		a.tokens[d] = b
	} else if dt := now.Sub(a.refill[d]).Seconds(); dt > 0 {
		a.tokens[d] += rate * dt
	}
	if a.tokens[d] > b {
		a.tokens[d] = b
	}
	a.refill[d] = now
}

func (s *Shaper) refillGlobal(d Dir, rate float64, now time.Time) {
	b := burst(rate)
	if s.gRefill[d].IsZero() {
		s.gTokens[d] = b
	} else if dt := now.Sub(s.gRefill[d]).Seconds(); dt > 0 {
		s.gTokens[d] += rate * dt
	}
	if s.gTokens[d] > b {
		s.gTokens[d] = b
	}
	s.gRefill[d] = now
}

// Count records a packet that was only observed (sniffing mode).
func (s *Shaper) Count(a *App, d Dir, size int) {
	a.passed[d].Add(int64(size))
	a.used.Add(int64(size))
}

// Admit decides what happens to a packet of the given size.
func (s *Shaper) Admit(a *App, d Dir, size int) Verdict {
	cfg := s.cfg.Load()
	r := a.rule.Load()
	if r != nil {
		if (cfg.Blocker && r.block(d)) ||
			(r.Quota.On && r.Quota.Bytes > 0 && a.used.Load() >= r.Quota.Bytes) {
			a.dropped[d].Add(int64(size))
			return Drop
		}
	}
	rate := appRate(cfg, r, d)
	capRate := cfg.capRate(d)
	if rate == 0 && capRate == 0 {
		s.Count(a, d, size)
		return Pass
	}

	now := s.now()
	s.mu.Lock()
	if a.q[d].len() == 0 {
		// Fast path: nothing is waiting ahead of this packet. With a global
		// cap, any backlog at all means the scheduler must arbitrate.
		ok := capRate == 0 || len(s.backlog[d]) == 0
		if ok && rate > 0 {
			s.refillApp(a, d, rate, now)
			ok = a.tokens[d] > 0
		}
		if ok && capRate > 0 {
			s.refillGlobal(d, capRate, now)
			ok = s.gTokens[d] > 0
		}
		if ok {
			if rate > 0 {
				a.tokens[d] -= float64(size)
			}
			if capRate > 0 {
				s.gTokens[d] -= float64(size)
			}
			s.mu.Unlock()
			s.Count(a, d, size)
			return Pass
		}
	} else {
		eff := rate
		if eff == 0 || (capRate > 0 && capRate < eff) {
			eff = capRate
		}
		if float64(a.qBytes[d])+float64(size) > eff*queueSeconds {
			s.mu.Unlock()
			a.dropped[d].Add(int64(size))
			return Drop
		}
	}
	s.mu.Unlock()
	return Queue
}

// Enqueue holds a packet that Admit answered Queue for.
func (s *Shaper) Enqueue(p *Packet) {
	p.at = s.now()
	a, d := p.App, p.Dir
	s.mu.Lock()
	a.q[d].push(p)
	a.qBytes[d] += int64(p.Size)
	if !a.backlog[d] {
		a.backlog[d] = true
		a.served[d] = p.at
		s.backlog[d] = append(s.backlog[d], a)
	}
	s.mu.Unlock()
}

// Tick releases every queued packet that the token buckets can pay for.
// Call it every few milliseconds.
func (s *Shaper) Tick() {
	now := s.now()
	cfg := s.cfg.Load()
	var out, dead []*Packet
	s.mu.Lock()
	for d := In; d <= Out; d++ {
		if len(s.backlog[d]) > 0 {
			out, dead = s.drain(d, cfg, now, out, dead)
		}
	}
	s.mu.Unlock()
	for _, p := range dead {
		p.App.dropped[p.Dir].Add(int64(p.Size))
	}
	for _, p := range out {
		s.Count(p.App, p.Dir, p.Size)
		s.release(p)
	}
}

func (s *Shaper) pop(a *App, d Dir) *Packet {
	p := a.q[d].pop()
	a.qBytes[d] -= int64(p.Size)
	return p
}

func (s *Shaper) drain(d Dir, cfg *Config, now time.Time, out, dead []*Packet) ([]*Packet, []*Packet) {
	bl := s.backlog[d]
	capRate := cfg.capRate(d)
	if capRate > 0 {
		s.refillGlobal(d, capRate, now)
	}

	// Housekeeping pass: expire stale packets, and let any app that has been
	// waiting too long send one packet so that low priority never means zero.
	for _, a := range bl {
		for a.q[d].len() > 0 && now.Sub(a.q[d].peek().at) > maxQueueAge {
			dead = append(dead, s.pop(a, d))
		}
		if capRate > 0 && a.q[d].len() > 0 && now.Sub(a.served[d]) > starvationGuard {
			rate := appRate(cfg, a.rule.Load(), d)
			if rate > 0 {
				s.refillApp(a, d, rate, now)
				if a.tokens[d] <= 0 {
					continue
				}
				a.tokens[d] -= float64(a.q[d].peek().Size)
			}
			p := s.pop(a, d)
			s.gTokens[d] -= float64(p.Size)
			a.served[d] = now
			out = append(out, p)
		}
	}

	exhausted := false
	for class := PrioCritical; class >= PrioLow && !exhausted; class-- {
		members := s.scratch[:0]
		for _, a := range bl {
			if a.q[d].len() > 0 && prioOf(cfg, a) == class {
				members = append(members, a)
			}
		}
		s.scratch = members
		n := len(members)
		if n == 0 {
			continue
		}
		start := s.rr[d] % n
		for progress := true; progress && !exhausted; {
			progress = false
			for i := 0; i < n; i++ {
				a := members[(start+i)%n]
				if a.q[d].len() == 0 {
					continue
				}
				rate := appRate(cfg, a.rule.Load(), d)
				if rate > 0 {
					s.refillApp(a, d, rate, now)
					if a.tokens[d] <= 0 {
						continue
					}
				}
				if capRate > 0 && s.gTokens[d] <= 0 {
					exhausted = true
					break
				}
				p := s.pop(a, d)
				if rate > 0 {
					a.tokens[d] -= float64(p.Size)
				}
				if capRate > 0 {
					s.gTokens[d] -= float64(p.Size)
				}
				a.served[d] = now
				out = append(out, p)
				progress = true
			}
		}
	}
	s.rr[d]++

	// Forget apps whose queue is now empty.
	keep := bl[:0]
	for _, a := range bl {
		if a.q[d].len() > 0 {
			keep = append(keep, a)
		} else {
			a.backlog[d] = false
		}
	}
	for i := len(keep); i < len(bl); i++ {
		bl[i] = nil
	}
	s.backlog[d] = keep
	return out, dead
}

// FlushAll releases every queued packet immediately, ignoring all limits.
// Engines call it before they stop intercepting.
func (s *Shaper) FlushAll() {
	var out []*Packet
	s.mu.Lock()
	for d := In; d <= Out; d++ {
		for _, a := range s.backlog[d] {
			for a.q[d].len() > 0 {
				out = append(out, s.pop(a, d))
			}
			a.backlog[d] = false
		}
		s.backlog[d] = nil
	}
	s.mu.Unlock()
	for _, p := range out {
		s.Count(p.App, p.Dir, p.Size)
		s.release(p)
	}
}

// Queued returns the number of bytes currently held for an app.
func (s *Shaper) Queued(a *App, d Dir) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return a.qBytes[d]
}

// fifo is a small slice-backed queue of packets.
type fifo struct {
	items []*Packet
	head  int
}

func (f *fifo) len() int       { return len(f.items) - f.head }
func (f *fifo) peek() *Packet  { return f.items[f.head] }
func (f *fifo) push(p *Packet) { f.items = append(f.items, p) }

func (f *fifo) pop() *Packet {
	p := f.items[f.head]
	f.items[f.head] = nil
	f.head++
	if f.head == len(f.items) {
		f.items = f.items[:0]
		f.head = 0
	} else if f.head > 256 && f.head*2 > len(f.items) {
		n := copy(f.items, f.items[f.head:])
		for i := n; i < len(f.items); i++ {
			f.items[i] = nil
		}
		f.items = f.items[:n]
		f.head = 0
	}
	return p
}
