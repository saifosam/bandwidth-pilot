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
// so limits, blocks, priorities, quotas, schedules and website rules all
// behave as they would live.
type demoEngine struct {
	s       *shaper.Shaper
	opts    *Options
	flows   *flowTable
	running atomic.Bool
	stop    chan struct{}
	wg      sync.WaitGroup
	rng     *rand.Rand
	start   time.Time

	mu   sync.Mutex // guards apps (a new app turns up after a while)
	apps []*demoApp
}

type demoApp struct {
	spec   demoSpec
	app    *shaper.App
	pids   []uint32
	phase  float64
	flows  []*flow
	back   [2]float64 // congestion back-off factor, 0..1
	acc    [2]float64
	joined bool
}

type demoSpec struct {
	name, path     string
	in, out        float64 // target bytes/s
	wobble, period float64 // how unsteady, and seconds per cycle
	conns          int
	window         bool
	procs          int      // number of processes
	hosts          []string // where its connections go
}

var demoSpecs = []demoSpec{
	{"Steam", `C:\Program Files (x86)\Steam\steam.exe`, 5_200_000, 60_000, 0.25, 40, 8, true, 2,
		[]string{"cache4-fra1.steamcontent.com", "cache2-fra1.steamcontent.com", "api.steampowered.com"}},
	{"Google Chrome", `C:\Program Files\Google\Chrome\Application\chrome.exe`, 900_000, 70_000, 0.9, 9, 14, true, 4,
		[]string{"rr3---sn-4g5e6nsz.googlevideo.com", "www.youtube.com", "i.ytimg.com", "www.google.com", "github.com", "fonts.gstatic.com"}},
	{"Zoom Meetings", `C:\Users\you\AppData\Roaming\Zoom\bin\Zoom.exe`, 310_000, 240_000, 0.2, 30, 4, true, 1,
		[]string{"zoomfrn93mmr.zoom.us", "us05web.zoom.us"}},
	{"OneDrive", `C:\Program Files\Microsoft OneDrive\OneDrive.exe`, 8_000, 1_400_000, 0.5, 25, 3, false, 1,
		[]string{"api.onedrive.com", "my.microsoftpersonalcontent.com"}},
	{"Discord", `C:\Users\you\AppData\Local\Discord\app\Discord.exe`, 46_000, 31_000, 0.6, 12, 5, true, 3,
		[]string{"gateway.discord.gg", "cdn.discordapp.com", "media.discordapp.net"}},
	{"Spotify", `C:\Users\you\AppData\Roaming\Spotify\Spotify.exe`, 40_000, 2_000, 0.8, 18, 3, true, 2,
		[]string{"audio-ak-spotify-com.akamaized.net", "spclient.wg.spotify.com"}},
	{"Visual Studio Code", `C:\Users\you\AppData\Local\Programs\Microsoft VS Code\Code.exe`, 6_000, 2_500, 0.9, 20, 4, true, 3,
		[]string{"marketplace.visualstudio.com", "api.github.com"}},
	{"Windows Update", `C:\Windows\System32\svchost.exe`, 650_000, 9_000, 0.95, 60, 6, false, 1,
		[]string{"7.tlu.dl.delivery.mp.microsoft.com", "fe3cr.delivery.mp.microsoft.com"}},
	{"qBittorrent", `C:\Program Files\qBittorrent\qbittorrent.exe`, 2_400_000, 800_000, 0.4, 35, 22, true, 1, nil},
	{"Microsoft Teams", `C:\Program Files\WindowsApps\MSTeams\ms-teams.exe`, 12_000, 6_000, 0.7, 15, 5, true, 2,
		[]string{"teams.microsoft.com", "statics.teams.cdn.office.net"}},
	{"Windows Defender Antimalware", `C:\ProgramData\Microsoft\Windows Defender\Platform\MsMpEng.exe`, 3_000, 1_200, 0.9, 30, 2, false, 1,
		[]string{"wdcp.microsoft.com"}},
	{"Host Process for Windows Services", `C:\Windows\System32\svchost.exe -k netsvcs`, 4_000, 2_000, 0.8, 20, 4, false, 1,
		[]string{"settings-win.data.microsoft.com", "time.windows.com"}},
	{"Python", `C:\Python312\python.exe`, 900, 400, 0.9, 10, 1, false, 1, []string{"pypi.org"}},
	{"Node.js JavaScript Runtime", `C:\Program Files\nodejs\node.exe`, 0, 0, 0, 10, 2, false, 1, []string{"registry.npmjs.org"}},
}

// An app that is not running at first, so a "new app wants to connect"
// prompt can be seen in the demo.
var demoLateSpec = demoSpec{"Epic Games Launcher", `C:\Program Files (x86)\Epic Games\Launcher\Portal\Binaries\Win64\EpicGamesLauncher.exe`,
	1_800_000, 20_000, 0.3, 30, 5, true, 1, []string{"download.epicgames.com", "launcher-public-service-prod06.ol.epicgames.com"}}

const demoLateAfter = 25 * time.Second

// NewDemo returns the simulated engine.
func NewDemo(s *shaper.Shaper, opts *Options) Engine {
	e := &demoEngine{s: s, opts: opts, flows: newFlowTable(), rng: rand.New(rand.NewSource(7))}
	for i, sp := range demoSpecs {
		e.apps = append(e.apps, e.newApp(sp, uint32(1200+i*412)))
	}
	return e
}

func (e *demoEngine) newApp(sp demoSpec, pid uint32) *demoApp {
	d := &demoApp{spec: sp, app: e.s.App(lower(sp.path), sp.name, sp.path), phase: e.rng.Float64() * 6.28}
	for p := 0; p < sp.procs; p++ {
		d.pids = append(d.pids, pid+uint32(p*4))
	}
	d.back[0], d.back[1] = 1, 1
	for c := 0; c < sp.conns; c++ {
		d.flows = append(d.flows, e.newFlow(d, c))
	}
	return d
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

func (e *demoEngine) newFlow(d *demoApp, n int) *flow {
	for {
		net := demoNets[e.rng.Intn(len(demoNets))]
		k := flowKey{proto: protoTCP, lport: uint16(49152 + e.rng.Intn(16000)), rport: 443}
		if e.rng.Intn(6) == 0 {
			k.proto, k.rport = protoUDP, 443
		}
		if len(d.spec.hosts) == 0 { // peer-to-peer
			k.rport = uint16(6881 + e.rng.Intn(2000))
		}
		f := &flow{key: k}
		addr := netip.AddrFrom4([4]byte{net[0], net[1], net[2], byte(1 + e.rng.Intn(250))})
		f.remote.Store(&addr)
		if len(d.spec.hosts) > 0 && e.opts != nil && e.opts.Sites != nil {
			// Pretend we saw the DNS answer that led to this connection.
			e.opts.Sites.Remember(addr, d.spec.hosts[n%len(d.spec.hosts)])
		}
		pid := d.pids[n%len(d.pids)]
		f.pid.Store(pid)
		f.app.Store(e.opts.splitApp(e.s, d.app, pid))
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
	e.start = time.Now()
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

func (e *demoEngine) snapshotApps() []*demoApp {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]*demoApp(nil), e.apps...)
}

func (e *demoEngine) loop() {
	defer e.wg.Done()
	const step = 10 * time.Millisecond
	const pkt = 1400
	t := time.NewTicker(step)
	defer t.Stop()
	for {
		select {
		case <-e.stop:
			return
		case now := <-t.C:
			el := now.Sub(e.start).Seconds()
			ms := now.UnixMilli()
			for _, d := range e.snapshotApps() {
				// A slow wave plus jitter makes each app's traffic look alive.
				wave := 0.5 + 0.5*math.Sin(el*6.28/d.spec.period+d.phase)
				level := (1 - d.spec.wobble) + d.spec.wobble*wave*wave*1.6
				level *= 0.85 + 0.3*e.rng.Float64()
				for dir := shaper.In; dir <= shaper.Out; dir++ {
					target := d.spec.in
					if dir == shaper.Out {
						target = d.spec.out
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
						app := f.app.Load()
						remote := *f.remote.Load()
						via := e.opts.route(f, remote)
						v := admit(e.s, app, via, dir, pkt)
						e.opts.logConn(f, app, via, remote, true, v == shaper.Drop)
						switch v {
						case shaper.Pass:
							f.bytes[dir].Add(pkt)
						case shaper.Queue:
							f.bytes[dir].Add(pkt)
							p := &shaper.Packet{App: app, Dir: dir, Size: pkt}
							if via != nil {
								p.App, p.Also = via, app
							}
							e.s.Enqueue(p)
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
	ms := now.UnixMilli()

	// A new app starts up after a while.
	e.mu.Lock()
	late := !e.start.IsZero() && now.Sub(e.start) > demoLateAfter && len(e.apps) == len(demoSpecs)
	e.mu.Unlock()
	if late {
		d := e.newApp(demoLateSpec, 9100)
		e.mu.Lock()
		e.apps = append(e.apps, d)
		e.mu.Unlock()
	}

	for _, d := range e.snapshotApps() {
		// Replace a connection now and then so the log keeps moving.
		if len(d.flows) > 1 && e.rng.Intn(6) == 0 {
			i := 1 + e.rng.Intn(len(d.flows)-1)
			d.flows[i].closed.Store(true)
			d.flows[i] = e.newFlow(d, i)
		}
		// Keep idle demo connections from expiring.
		for _, f := range d.flows {
			if ms-f.last.Load() > 20_000 {
				f.last.Store(ms - 5_000)
			}
		}
	}
	e.flows.housekeep(now)
}

func (e *demoEngine) Resplit() {
	byID := map[string]*demoApp{}
	for _, d := range e.snapshotApps() {
		byID[d.app.ID] = d
	}
	e.flows.each(func(f *flow) {
		a := f.app.Load()
		if a == nil {
			return
		}
		parent := a.ID
		if a.Parent != "" {
			parent = a.Parent
		}
		if d := byID[parent]; d != nil {
			f.app.Store(e.opts.splitApp(e.s, d.app, f.pid.Load()))
		}
	})
}

func (e *demoEngine) Connections(appID string) []Conn { return e.flows.connections(appID) }
func (e *demoEngine) Summary() map[string]AppProcs    { return e.flows.summary() }
func (e *demoEngine) Icon(string) string              { return "" }

func (e *demoEngine) Windowed() map[string]bool {
	out := map[string]bool{}
	for _, d := range e.snapshotApps() {
		if d.spec.window {
			out[d.app.ID] = true
		}
	}
	return out
}
