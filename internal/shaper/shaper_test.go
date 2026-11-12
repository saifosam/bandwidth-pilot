package shaper

import (
	"math"
	"testing"
	"time"
)

// sim drives a shaper with a fake clock. Each app offers traffic at a fixed
// rate in 1400-byte packets; the shaper is ticked every 2 ms.
type sim struct {
	t   *testing.T
	s   *Shaper
	now time.Time
}

func newSim(t *testing.T) *sim {
	m := &sim{t: t, now: time.Unix(1_700_000_000, 0)}
	m.s = New(func(*Packet) {})
	m.s.SetClock(func() time.Time { return m.now })
	return m
}

type offer struct {
	app  *App
	dir  Dir
	rate float64 // bytes per second offered
	acc  float64
}

func (m *sim) run(d time.Duration, offers ...*offer) {
	const pkt = 1400
	step := time.Millisecond
	for el := time.Duration(0); el < d; el += step {
		m.now = m.now.Add(step)
		for _, o := range offers {
			o.acc += o.rate * step.Seconds()
			for o.acc >= pkt {
				o.acc -= pkt
				if m.s.Admit(o.app, o.dir, pkt) == Queue {
					m.s.Enqueue(&Packet{App: o.app, Dir: o.dir, Size: pkt})
				}
			}
		}
		if el%(2*time.Millisecond) == 0 {
			m.s.Tick()
		}
	}
}

func within(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > want*tol {
		t.Fatalf("%s: got %.0f, want %.0f (+/- %.0f%%)", what, got, want, tol*100)
	}
}

func TestUnlimitedPassesEverything(t *testing.T) {
	m := newSim(t)
	a := m.s.App("a", "A", "")
	m.run(5*time.Second, &offer{app: a, dir: In, rate: 2_000_000})
	within(t, "passed", float64(a.Passed(In)), 10_000_000, 0.01)
	if a.Dropped(In) != 0 {
		t.Fatalf("dropped %d bytes with no rule", a.Dropped(In))
	}
	if m.s.NeedsIntercept() {
		t.Fatal("intercept requested with no rules")
	}
}

func TestPerAppLimit(t *testing.T) {
	for _, rate := range []int64{20_000, 100_000, 1_000_000} {
		m := newSim(t)
		a := m.s.App("a", "A", "")
		m.s.SetRule(a, Rule{Priority: PrioNormal, LimitIn: Limit{On: true, Bps: rate}})
		if !m.s.NeedsIntercept() {
			t.Fatal("limit rule should request intercept")
		}
		m.run(10*time.Second, &offer{app: a, dir: In, rate: 5_000_000}, &offer{app: a, dir: Out, rate: 300_000})
		within(t, "limited download", float64(a.Passed(In)), float64(rate)*10, 0.05)
		within(t, "unlimited upload", float64(a.Passed(Out)), 3_000_000, 0.01)
	}
}

func TestLimiterSwitchOff(t *testing.T) {
	m := newSim(t)
	a := m.s.App("a", "A", "")
	m.s.SetRule(a, Rule{Priority: PrioNormal, LimitIn: Limit{On: true, Bps: 50_000}})
	cfg := m.s.Config()
	cfg.Limiter = false
	m.s.SetConfig(cfg)
	if m.s.NeedsIntercept() {
		t.Fatal("limiter is off, nothing should need intercepting")
	}
	m.run(2*time.Second, &offer{app: a, dir: In, rate: 1_000_000})
	within(t, "passed", float64(a.Passed(In)), 2_000_000, 0.01)
}

func TestBlock(t *testing.T) {
	m := newSim(t)
	a := m.s.App("a", "A", "")
	m.s.SetRule(a, Rule{Priority: PrioNormal, BlockOut: true})
	m.run(2*time.Second, &offer{app: a, dir: In, rate: 500_000}, &offer{app: a, dir: Out, rate: 500_000})
	if a.Passed(Out) != 0 {
		t.Fatalf("blocked direction passed %d bytes", a.Passed(Out))
	}
	within(t, "open direction", float64(a.Passed(In)), 1_000_000, 0.01)

	cfg := m.s.Config()
	cfg.Blocker = false
	m.s.SetConfig(cfg)
	m.run(time.Second, &offer{app: a, dir: Out, rate: 500_000})
	within(t, "blocker off", float64(a.Passed(Out)), 500_000, 0.02)
}

func TestQuota(t *testing.T) {
	m := newSim(t)
	a := m.s.App("a", "A", "")
	m.s.SetRule(a, Rule{Priority: PrioNormal, Quota: Quota{On: true, Bytes: 1_000_000}})
	m.run(5*time.Second, &offer{app: a, dir: In, rate: 500_000})
	within(t, "quota", float64(a.Passed(In)), 1_000_000, 0.01)
	if !a.QuotaExceeded() {
		t.Fatal("quota should be exceeded")
	}
	a.SetUsed(0) // midnight
	m.run(time.Second, &offer{app: a, dir: In, rate: 500_000})
	within(t, "after reset", float64(a.Passed(In)), 1_500_000, 0.01)
}

func TestGlobalCap(t *testing.T) {
	m := newSim(t)
	a := m.s.App("a", "A", "")
	b := m.s.App("b", "B", "")
	cfg := m.s.Config()
	cfg.CapOn, cfg.CapIn = true, 1_000_000
	m.s.SetConfig(cfg)
	m.run(10*time.Second, &offer{app: a, dir: In, rate: 3_000_000}, &offer{app: b, dir: In, rate: 3_000_000})
	total := float64(a.Passed(In) + b.Passed(In))
	within(t, "cap total", total, 10_000_000, 0.05)
	// Same priority: the cap is shared evenly.
	within(t, "fair share", float64(a.Passed(In)), total/2, 0.10)
}

func TestPriorityUnderCap(t *testing.T) {
	m := newSim(t)
	game := m.s.App("game", "Game", "")
	sync := m.s.App("sync", "Sync", "")
	m.s.SetRule(game, Rule{Priority: PrioHigh})
	m.s.SetRule(sync, Rule{Priority: PrioLow})
	cfg := m.s.Config()
	cfg.CapOn, cfg.CapIn = true, 1_000_000
	m.s.SetConfig(cfg)
	// The game wants 600 KB/s, the sync job wants everything.
	m.run(10*time.Second, &offer{app: game, dir: In, rate: 600_000}, &offer{app: sync, dir: In, rate: 5_000_000})
	g, s := float64(game.Passed(In)), float64(sync.Passed(In))
	within(t, "high priority gets what it asks for", g, 6_000_000, 0.05)
	within(t, "low priority gets the remainder", s, 4_000_000, 0.10)
	within(t, "cap total", g+s, 10_000_000, 0.05)

	// Both greedy: high takes nearly everything, low is not starved to zero.
	m2 := newSim(t)
	hi := m2.s.App("hi", "Hi", "")
	lo := m2.s.App("lo", "Lo", "")
	m2.s.SetRule(hi, Rule{Priority: PrioCritical})
	m2.s.SetRule(lo, Rule{Priority: PrioLow})
	m2.s.SetConfig(cfg)
	m2.run(10*time.Second, &offer{app: hi, dir: In, rate: 5_000_000}, &offer{app: lo, dir: In, rate: 5_000_000})
	h, l := float64(hi.Passed(In)), float64(lo.Passed(In))
	if h < 9_000_000 {
		t.Fatalf("critical app got only %.0f of 10 MB", h)
	}
	if l <= 0 || l > 600_000 {
		t.Fatalf("low app got %.0f bytes; want a trickle", l)
	}
	within(t, "cap total", h+l, 10_000_000, 0.05)

	// Priorities switched off: back to an even split.
	cfg.Priorities = false
	m3 := newSim(t)
	x := m3.s.App("x", "X", "")
	y := m3.s.App("y", "Y", "")
	m3.s.SetRule(x, Rule{Priority: PrioCritical})
	m3.s.SetRule(y, Rule{Priority: PrioLow})
	m3.s.SetConfig(cfg)
	m3.run(10*time.Second, &offer{app: x, dir: In, rate: 5_000_000}, &offer{app: y, dir: In, rate: 5_000_000})
	within(t, "even split with priorities off", float64(x.Passed(In)), 5_000_000, 0.10)
}

func TestLimitAndCapTogether(t *testing.T) {
	m := newSim(t)
	a := m.s.App("a", "A", "")
	b := m.s.App("b", "B", "")
	m.s.SetRule(a, Rule{Priority: PrioNormal, LimitIn: Limit{On: true, Bps: 100_000}})
	cfg := m.s.Config()
	cfg.CapOn, cfg.CapIn = true, 1_000_000
	m.s.SetConfig(cfg)
	m.run(10*time.Second, &offer{app: a, dir: In, rate: 3_000_000}, &offer{app: b, dir: In, rate: 3_000_000})
	within(t, "limited app", float64(a.Passed(In)), 1_000_000, 0.06)
	within(t, "cap total", float64(a.Passed(In)+b.Passed(In)), 10_000_000, 0.05)
}

func TestFlushAllReleasesEverything(t *testing.T) {
	released := 0
	m := newSim(t)
	m.s.release = func(*Packet) { released++ }
	a := m.s.App("a", "A", "")
	m.s.SetRule(a, Rule{Priority: PrioNormal, LimitIn: Limit{On: true, Bps: 10_000}})
	for i := 0; i < 4; i++ {
		if m.s.Admit(a, In, 1400) == Queue {
			m.s.Enqueue(&Packet{App: a, Dir: In, Size: 1400})
		}
	}
	held := m.s.Queued(a, In)
	if held == 0 {
		t.Fatal("expected packets to be held")
	}
	m.s.FlushAll()
	if m.s.Queued(a, In) != 0 || released == 0 {
		t.Fatalf("flush left %d bytes queued, released %d", m.s.Queued(a, In), released)
	}
}

func TestBigPacketsStillObeyLimit(t *testing.T) {
	// Offloaded segments can be ~64 KB, far larger than a small bucket.
	m := newSim(t)
	a := m.s.App("a", "A", "")
	m.s.SetRule(a, Rule{Priority: PrioNormal, LimitOut: Limit{On: true, Bps: 200_000}})
	for el := 0; el < 10_000; el++ {
		m.now = m.now.Add(time.Millisecond)
		if el%20 == 0 { // 50 x 60 KB per second = 3 MB/s offered
			if m.s.Admit(a, Out, 60_000) == Queue {
				m.s.Enqueue(&Packet{App: a, Dir: Out, Size: 60_000})
			}
		}
		m.s.Tick()
	}
	within(t, "big packets", float64(a.Passed(Out)), 2_000_000, 0.08)
}
