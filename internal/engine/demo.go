package engine

import (
	"math"
	"math/rand"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"bandwidthpilot/internal/shaper"
)

// demoEngine simulates a busy PC. Its traffic goes through the real shaper,
// so limits, blocks, priorities and quotas all behave as they would live.
type demoEngine struct {
	s       *shaper.Shaper
	flows   *flowTable
	apps    []*demoApp
	running atomic.Bool
	stop    chan struct{}
	wg      sync.WaitGroup
	rng     *rand.Rand
}

type demoApp struct {
	app    *shaper.App
	window bool
	pid    uint32
	in     float64 // target download, bytes/s
	out    float64 // target upload, bytes/s
	wobble float64 // 0..1, how unsteady the traffic is
	period float64 // seconds per traffic cycle
	phase  float64
	flows  []*flow
	back   [2]float64 // congestion back-off factor, 0..1
	acc    [2]float64
}

// NewDemo returns the simulated engine.
func NewDemo(s *shaper.Shaper) Engine {
	e := &demoEngine{s: s, flows: newFlowTable(), rng: rand.New(rand.NewSource(7))}
	type spec struct {
		name, path     string
		in, out        float64
		wobble, period float64
		conns          int
		window         bool
	}
	specs := []spec{
		{"Steam", `C:\Program Files (x86)\Steam\steam.exe`, 5_200_000, 60_000, 0.25, 40, 8, true},
		{"Google Chrome", `C:\Program Files\Google\Chrome\Application\chrome.exe`, 900_000, 70_000, 0.9, 9, 14, true},
		{"Zoom Meetings", `C:\Users\you\AppData\Roaming\Zoom\bin\Zoom.exe`, 310_000, 240_000, 0.2, 30, 4, true},
		{"OneDrive", `C:\Program Files\Microsoft OneDrive\OneDrive.exe`, 8_000, 1_400_000, 0.5, 25, 3, false},
		{"Discord", `C:\Users\you\AppData\Local\Discord\app\Discord.exe`, 46_000, 31_000, 0.6, 12, 5, true},
		{"Spotify", `C:\Users\you\AppData\Roaming\Spotify\Spotify.exe`, 40_000, 2_000, 0.8, 18, 3, true},
		{"Visual Studio Code", `C:\Users\you\AppData\Local\Programs\Microsoft VS Code\Code.exe`, 6_000, 2_500, 0.9, 20, 4, true},
		{"Windows Update", `C:\Windows\System32\svchost.exe`, 650_000, 9_000, 0.95, 60, 6, false},
		{"qBittorrent", `C:\Program Files\qBittorrent\qbittorrent.exe`, 2_400_000, 800_000, 0.4, 35, 22, true},
		{"Microsoft Teams", `C:\Program Files\WindowsApps\MSTeams\ms-teams.exe`, 12_000, 6_000, 0.7, 15, 5, true},
		{"Windows Defender Antimalware", `C:\ProgramData\Microsoft\Windows Defender\Platform\MsMpEng.exe`, 3_000, 1_200, 0.9, 30, 2, false},
		{"Host Process for Windows Services", `C:\Windows\System32\svchost.exe -k netsvcs`, 4_000, 2_000, 0.8, 20, 4, false},
		{"Python", `C:\Python312\python.exe`, 900, 400, 0.9, 10, 1, false},
		{"Node.js JavaScript Runtime", `C:\Program Files\nodejs\node.exe`, 0, 0, 0, 10, 2, false},
	}
	for i, sp := range specs {
		id := lower(sp.path)
		a := s.App(id, sp.name, sp.path)
		d := &demoApp{app: a, window: sp.window, pid: uint32(1200 + i*412), in: sp.in, out: sp.out,
			wobble: sp.wobble, period: sp.period, phase: e.rng.Float64() * 6.28}
		d.back[0], d.back[1] = 1, 1
		for c := 0; c < sp.conns; c++ {
			d.flows = append(d.flows, e.newFlow(d))
		}
		e.apps = append(e.apps, d)
	}
	return e
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

var demoNets = [][3]byte{{203, 0, 113}, {198, 51, 100}, {192, 0, 2}}

func (e *demoEngine) newFlow(d *demoApp) *flow {
	for {
		n := demoNets[e.rng.Intn(len(demoNets))]
		k := flowKey{proto: protoTCP, lport: uint16(49152 + e.rng.Intn(16000)), rport: 443}
		if e.rng.Intn(6) == 0 {
			k.proto, k.rport = protoUDP, uint16(3478+e.rng.Intn(4))
		}
		f := &flow{key: k}
		addr := netip.AddrFrom4([4]byte{n[0], n[1], n[2], byte(1 + e.rng.Intn(250))})
		f.remote.Store(&addr)
		f.app.Store(d.app)
		f.pid.Store(d.pid)
		f.resolved.Store(true)
		f.last.Store(time.Now().UnixMilli())
		if e.flows.put(f) == f {
			return f
		}
	}
}

func (e *demoEngine) Start() error {
	if e.running.Swap(true) {
		return nil
	}
	e.stop = make(chan struct{})
	e.wg.Add(1)
	go e.loop()
	return nil
}

func (e *demoEngine) Stop() {
	if !e.running.Swap(false) {
		return
	}
	close(e.stop)
	e.wg.Wait()
}

func (e *demoEngine) loop() {
	defer e.wg.Done()
	const step = 10 * time.Millisecond
	const pkt = 1400
	t := time.NewTicker(step)
	defer t.Stop()
	start := time.Now()
	for {
		select {
		case <-e.stop:
			return
		case now := <-t.C:
			el := now.Sub(start).Seconds()
			ms := now.UnixMilli()
			for _, d := range e.apps {
				// A slow wave plus jitter makes each app's traffic look alive.
				wave := 0.5 + 0.5*math.Sin(el*6.28/d.period+d.phase)
				level := (1 - d.wobble) + d.wobble*wave*wave*1.6
				level *= 0.85 + 0.3*e.rng.Float64()
				for dir := shaper.In; dir <= shaper.Out; dir++ {
					target := d.in
					if dir == shaper.Out {
						target = d.out
					}
					if target == 0 || len(d.flows) == 0 {
						continue
					}
					d.acc[dir] += target * level * d.back[dir] * step.Seconds()
					for d.acc[dir] >= pkt {
						d.acc[dir] -= pkt
						// Most traffic rides the first few connections.
						f := d.flows[int(math.Abs(e.rng.NormFloat64())*1.2)%len(d.flows)]
						f.last.Store(ms)
						switch e.s.Admit(d.app, dir, pkt) {
						case shaper.Pass:
							f.bytes[dir].Add(pkt)
						case shaper.Queue:
							f.bytes[dir].Add(pkt)
							e.s.Enqueue(&shaper.Packet{App: d.app, Dir: dir, Size: pkt})
						case shaper.Drop:
							// Senders slow down when packets go missing, like TCP.
							d.back[dir] *= 0.9
							if d.back[dir] < 0.004 {
								d.back[dir] = 0.004
							}
						}
					}
					d.back[dir] = math.Min(1, d.back[dir]*1.004+0.0004)
				}
			}
		}
	}
}

func (e *demoEngine) Status() Status {
	return Status{Mode: "demo", Driver: "Simulated traffic", Running: e.running.Load(),
		Intercepting: e.s.NeedsIntercept()}
}

func (e *demoEngine) Release(*shaper.Packet) {}

func (e *demoEngine) Housekeep() {
	now := time.Now()
	// Keep idle demo connections from expiring, and churn one now and then.
	ms := now.UnixMilli()
	for _, d := range e.apps {
		for _, f := range d.flows {
			if ms-f.last.Load() > 20_000 {
				f.last.Store(ms - 5_000)
			}
		}
	}
	e.flows.housekeep(now)
}

func (e *demoEngine) Connections(appID string) []Conn { return e.flows.connections(appID) }
func (e *demoEngine) Summary() map[string]AppProcs    { return e.flows.summary() }
func (e *demoEngine) Icon(string) string              { return "" }

func (e *demoEngine) Windowed() map[string]bool {
	out := map[string]bool{}
	for _, d := range e.apps {
		if d.window {
			out[d.app.ID] = true
		}
	}
	return out
}
