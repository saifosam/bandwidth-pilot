// Package core ties the shaper, an engine and the on-disk stores together
// and exposes everything the UI needs through a small RPC surface.
package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"bandwidthpilot/internal/engine"
	"bandwidthpilot/internal/shaper"
	"bandwidthpilot/internal/sites"
)

// AppView is one row of the activity list.
type AppView struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	Path       string      `json:"path"`
	Kind       string      `json:"kind"` // "app", "background" or "windows"
	Window     bool        `json:"window"`
	PIDs       []uint32    `json:"pids"`
	Conns      int         `json:"conns"`
	RateIn     int64       `json:"rateIn"`
	RateOut    int64       `json:"rateOut"`
	TotalIn    int64       `json:"totalIn"`
	TotalOut   int64       `json:"totalOut"`
	TodayIn    int64       `json:"todayIn"`
	TodayOut   int64       `json:"todayOut"`
	DroppedIn  int64       `json:"droppedIn"`
	DroppedOut int64       `json:"droppedOut"`
	Online     bool        `json:"online"`
	Parent     string      `json:"parent"`
	Split      bool        `json:"split"`
	InForce    bool        `json:"inForce"`
	HasRule    bool        `json:"hasRule"`
	QuotaHit   bool        `json:"quotaHit"`
	Rule       shaper.Rule `json:"rule"`
}

// Snapshot is the complete UI state, polled once a second.
type Snapshot struct {
	Host     string        `json:"host"`
	OS       string        `json:"os"`
	Version  string        `json:"version"`
	Engine   engine.Status `json:"engine"`
	Config   shaper.Config `json:"config"`
	Apps     []AppView     `json:"apps"`
	RateIn   int64         `json:"rateIn"`
	RateOut  int64         `json:"rateOut"`
	PeakIn   int64         `json:"peakIn"`
	PeakOut  int64         `json:"peakOut"`
	TotalIn  int64         `json:"totalIn"`
	TotalOut int64         `json:"totalOut"`
	Seq      int64         `json:"seq"`
	Ask      bool          `json:"ask"`
	Pending  []Pending     `json:"pending"`
	Sites    []SiteView    `json:"sites"`
	LogSeq   int64         `json:"logSeq"`
}

type appStat struct {
	prev       [2]int64
	rate       [2]int64
	lastActive time.Time
	seeded     bool
}

// Service is the application backend.
type Service struct {
	S       *shaper.Shaper
	Version string
	Sites   *sites.Table
	// Opts is what engines need to report connections and honour splits.
	Opts *engine.Options
	// OnPending is called (in a goroutine) when a new app waits for approval.
	OnPending func(name string)
	// Hooks are extra RPC methods supplied by the platform shell.
	Hooks map[string]func(json.RawMessage) (any, error)

	f features

	mu         sync.Mutex
	eng        engine.Engine
	stats      map[string]*appStat
	lastWindow map[string]time.Time
	rate       [2]int64
	peak       [2]int64
	total      [2]int64
	seq        int64
	host       string
	dir        string
	logf       engine.Logf
	usage      *usageStore
	prefs      json.RawMessage
	stop       chan struct{}
	stopped    sync.WaitGroup
}

type savedRule struct {
	Name string      `json:"name"`
	Path string      `json:"path"`
	Rule shaper.Rule `json:"rule"`
}

type savedState struct {
	Config shaper.Config        `json:"config"`
	Rules  map[string]savedRule `json:"rules"`
	Ask    bool                 `json:"askNewApps"`
	Split  []string             `json:"split"`
	Sites  []sites.Site         `json:"sites"`
	Prefs  json.RawMessage      `json:"prefs,omitempty"` // UI preferences, opaque to the backend
}

// New creates the service and loads saved rules from dir.
func New(dir, version string, logf engine.Logf) *Service {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "This PC"
	}
	sv := &Service{Version: version, stats: map[string]*appStat{}, lastWindow: map[string]time.Time{}, host: host, dir: dir, logf: logf,
		Sites: sites.New(), f: newFeatures(), Hooks: map[string]func(json.RawMessage) (any, error){}}
	sv.Opts = &engine.Options{Logf: logf, Sites: sv.Sites, Split: sv.isSplit, OnConn: sv.addLog}
	sv.S = shaper.New(func(p *shaper.Packet) {
		// The engine can be swapped at runtime, so look it up per call.
		if e := sv.engine(); e != nil {
			e.Release(p)
		}
	})
	sv.S.OnIntercept(func(on bool) {
		if e, ok := sv.engine().(interface{ SetIntercept(bool) }); ok {
			e.SetIntercept(on)
		}
	})
	sv.S.OnNewApp(sv.onNewApp)
	sv.usage = loadUsage(filepath.Join(dir, "usage.json"))
	sv.loadKnown()
	sv.loadRules()
	return sv
}

func (sv *Service) engine() engine.Engine {
	sv.mu.Lock()
	defer sv.mu.Unlock()
	return sv.eng
}

func (sv *Service) rulesPath() string { return filepath.Join(sv.dir, "rules.json") }

func (sv *Service) loadRules() {
	b, err := os.ReadFile(sv.rulesPath())
	if err != nil {
		return
	}
	var st savedState
	if err := json.Unmarshal(b, &st); err != nil {
		sv.logf("rules.json is unreadable, starting fresh: %v", err)
		return
	}
	sv.S.SetConfig(st.Config)
	sv.prefs = st.Prefs
	sv.f.ask = st.Ask
	for _, id := range st.Split {
		sv.f.split[id] = true
	}
	sv.f.sites = st.Sites
	for id := range st.Rules {
		sv.f.known[id] = true // an app with a rule was approved long ago
	}
	sv.applySites()
	for id, r := range st.Rules {
		sv.S.SetRule(sv.S.App(id, r.Name, r.Path), r.Rule)
	}
}

func (sv *Service) saveRules() {
	sv.mu.Lock()
	prefs := sv.prefs
	sv.mu.Unlock()
	st := savedState{Config: sv.S.Config(), Rules: map[string]savedRule{}, Prefs: prefs}
	sv.f.mu.Lock()
	st.Ask = sv.f.ask
	for id := range sv.f.split {
		st.Split = append(st.Split, id)
	}
	sort.Strings(st.Split)
	st.Sites = append([]sites.Site{}, sv.f.sites...)
	sv.f.mu.Unlock()
	for _, a := range sv.S.Apps() {
		if a.Parent != "" {
			continue // per-process rules last only while the process runs
		}
		if r := a.Rule(); !r.IsDefault() {
			st.Rules[a.ID] = savedRule{Name: a.Name, Path: a.Path, Rule: r}
		}
	}
	writeJSON(sv.rulesPath(), st)
}

func writeJSON(path string, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, path)
	}
}

// UseEngine installs and starts an engine, stopping the previous one.
func (sv *Service) UseEngine(e engine.Engine) error {
	sv.mu.Lock()
	old := sv.eng
	sv.eng = e
	sv.mu.Unlock()
	if old != nil {
		old.Stop()
	}
	return e.Start()
}

// Run starts the background loops: the shaper scheduler and the 1 Hz stats tick.
func (sv *Service) Run() {
	sv.stop = make(chan struct{})
	sv.stopped.Add(2)
	go func() {
		defer sv.stopped.Done()
		for {
			select {
			case <-sv.stop:
				return
			default:
			}
			if sv.S.NeedsIntercept() {
				sv.S.Tick()
				time.Sleep(2 * time.Millisecond)
			} else {
				// Nothing is being shaped; still flush anything left over.
				sv.S.Tick()
				time.Sleep(50 * time.Millisecond)
			}
		}
	}()
	go func() {
		defer sv.stopped.Done()
		t := time.NewTicker(time.Second)
		defer t.Stop()
		n := 0
		for {
			select {
			case <-sv.stop:
				return
			case now := <-t.C:
				sv.tick(now)
				if n++; n%30 == 0 {
					sv.usage.save()
					sv.saveKnown()
				}
			}
		}
	}()
}

// Close stops everything and saves state.
func (sv *Service) Close() {
	if sv.stop != nil {
		close(sv.stop)
		sv.stopped.Wait()
		sv.stop = nil
	}
	if e := sv.engine(); e != nil {
		e.Stop()
	}
	sv.usage.save()
	sv.saveKnown()
	sv.saveRules()
}

// tick samples every app's counters once a second.
func (sv *Service) tick(now time.Time) {
	if e := sv.engine(); e != nil {
		e.Housekeep()
	}
	sv.S.ApplySchedules()
	day := now.Format("2006-01-02")
	rolled := sv.usage.roll(day)
	apps := sv.S.Apps()

	sv.mu.Lock()
	defer sv.mu.Unlock()
	var sum [2]int64
	for _, a := range apps {
		st := sv.stats[a.ID]
		if st == nil {
			st = &appStat{}
			sv.stats[a.ID] = st
		}
		if !st.seeded {
			// First sight of this app today: count what earlier sessions used
			// against its quota.
			st.seeded = true
			in, out := sv.usage.today(a.ID)
			a.SetUsed(a.Used() + in + out)
		} else if rolled {
			a.SetUsed(0)
		}
		var delta [2]int64
		for d := 0; d < 2; d++ {
			cur := a.Passed(shaper.Dir(d))
			delta[d] = cur - st.prev[d]
			st.prev[d] = cur
			st.rate[d] = delta[d]
			if !strings.HasPrefix(a.ID, sitePrefix) {
				sum[d] += delta[d]
			}
		}
		if delta[0] > 0 || delta[1] > 0 {
			st.lastActive = now
			switch {
			case strings.HasPrefix(a.ID, sitePrefix):
				// Website rules are counted on the Websites tab, not as apps.
			case a.Parent != "":
				if p := sv.S.Lookup(a.Parent); p != nil {
					sv.usage.add(p.ID, p.Name, delta[0], delta[1])
				}
			default:
				sv.usage.add(a.ID, a.Name, delta[0], delta[1])
			}
		}
	}
	for d := 0; d < 2; d++ {
		sv.rate[d] = sum[d]
		sv.total[d] += sum[d]
		if sum[d] > sv.peak[d] {
			sv.peak[d] = sum[d]
		}
	}
	sv.seq++
}

// kindOf sorts an app into Task Manager's three groups. An app keeps the
// "app" group for a minute after its window closes, so rows do not jump
// between groups while windows are minimised to the tray and back.
func (sv *Service) kindOf(a *shaper.App, window bool) string {
	now := time.Now()
	if window {
		sv.lastWindow[a.ID] = now
		return "app"
	}
	if t, ok := sv.lastWindow[a.ID]; ok && now.Sub(t) < time.Minute {
		return "app"
	}
	p := strings.ToLower(a.Path)
	if a.ID == "system" || strings.HasPrefix(p, `c:\windows\`) || strings.Contains(p, `\windows defender\`) {
		return "windows"
	}
	return "background"
}

// Snapshot builds the UI state.
func (sv *Service) Snapshot() Snapshot {
	e := sv.engine()
	var summary map[string]engine.AppProcs
	var windowed map[string]bool
	status := engine.Status{Mode: "none"}
	if e != nil {
		summary = e.Summary()
		status = e.Status()
		windowed = e.Windowed()
	}
	apps := sv.S.Apps()
	now := time.Now()

	sv.mu.Lock()
	defer sv.mu.Unlock()
	snap := Snapshot{Host: sv.host, OS: runtime.GOOS, Version: sv.Version, Engine: status, Config: sv.S.Config(),
		RateIn: sv.rate[0], RateOut: sv.rate[1], PeakIn: sv.peak[0], PeakOut: sv.peak[1],
		TotalIn: sv.total[0], TotalOut: sv.total[1], Seq: sv.seq}
	snap.Pending = sv.pendingList()
	snap.Sites = sv.siteViews()
	sv.f.mu.Lock()
	snap.Ask, snap.LogSeq = sv.f.ask, sv.f.logSeq
	sv.f.mu.Unlock()
	for _, a := range apps {
		if strings.HasPrefix(a.ID, sitePrefix) {
			continue
		}
		st := sv.stats[a.ID]
		if st == nil {
			st = &appStat{}
		}
		r := a.Rule()
		p := summary[a.ID]
		tin, tout := sv.usage.today(a.ID)
		v := AppView{ID: a.ID, Name: a.Name, Path: a.Path, PIDs: p.PIDs, Conns: p.Conns,
			RateIn: st.rate[0], RateOut: st.rate[1],
			TotalIn: a.Passed(shaper.In), TotalOut: a.Passed(shaper.Out),
			TodayIn: tin, TodayOut: tout,
			DroppedIn: a.Dropped(shaper.In), DroppedOut: a.Dropped(shaper.Out),
			HasRule: !r.IsDefault(), QuotaHit: a.QuotaExceeded(), Rule: r}
		v.Parent = a.Parent
		v.InForce = a.InForce()
		if a.Parent != "" {
			// A process row belongs wherever its app does.
			if p := sv.S.Lookup(a.Parent); p != nil {
				v.Window = windowed[p.ID]
				v.Kind = sv.kindOf(p, v.Window)
			}
		} else {
			v.Split = sv.isSplit(a.ID)
			v.Window = windowed[a.ID]
			v.Kind = sv.kindOf(a, v.Window)
		}
		v.Online = p.Conns > 0 || (!st.lastActive.IsZero() && now.Sub(st.lastActive) < 60*time.Second)
		if v.PIDs == nil {
			v.PIDs = []uint32{}
		}
		// Apps that are gone and have no rule drop off the list; so do
		// process rows whose process has ended.
		if !v.Online && !v.HasRule && (v.TotalIn+v.TotalOut == 0 || v.Parent != "") {
			continue
		}
		snap.Apps = append(snap.Apps, v)
	}
	sort.Slice(snap.Apps, func(i, j int) bool { return snap.Apps[i].Name < snap.Apps[j].Name })
	if snap.Apps == nil {
		snap.Apps = []AppView{}
	}
	return snap
}

// Call dispatches one RPC from the UI.
func (sv *Service) Call(method string, params json.RawMessage) (any, error) {
	var p struct {
		ID     string          `json:"id"`
		Rule   *shaper.Rule    `json:"rule"`
		Config *shaper.Config  `json:"config"`
		Prefs  json.RawMessage `json:"prefs"`
		On     bool            `json:"on"`
		Allow  bool            `json:"allow"`
		Since  int64           `json:"since"`
		Site   *sites.Site     `json:"site"`
	}
	if len(params) > 0 && string(params) != "null" {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("bad parameters for %s: %w", method, err)
		}
	}
	switch method {
	case "snapshot":
		return sv.Snapshot(), nil

	case "connections":
		if e := sv.engine(); e != nil {
			if c := e.Connections(p.ID); c != nil {
				return c, nil
			}
		}
		return []engine.Conn{}, nil

	case "icon":
		if e := sv.engine(); e != nil {
			return e.Icon(p.ID), nil
		}
		return "", nil

	case "setRule":
		a := sv.S.Lookup(p.ID)
		if a == nil || p.Rule == nil {
			return nil, errors.New("unknown app")
		}
		if p.Rule.IsDefault() {
			sv.S.ClearRule(a)
		} else {
			sv.S.SetRule(a, *p.Rule)
		}
		sv.saveRules()
		return sv.Snapshot(), nil

	case "clearRule":
		if a := sv.S.Lookup(p.ID); a != nil {
			sv.S.ClearRule(a)
			sv.saveRules()
		}
		return sv.Snapshot(), nil

	case "clearAllRules":
		for _, a := range sv.S.Apps() {
			if !strings.HasPrefix(a.ID, sitePrefix) {
				sv.S.ClearRule(a)
			}
		}
		sv.saveRules()
		return sv.Snapshot(), nil

	case "setConfig":
		if p.Config == nil {
			return nil, errors.New("missing config")
		}
		sv.S.SetConfig(*p.Config)
		sv.saveRules()
		return sv.Snapshot(), nil

	case "prefs":
		sv.mu.Lock()
		defer sv.mu.Unlock()
		if len(sv.prefs) == 0 {
			return json.RawMessage("{}"), nil
		}
		return sv.prefs, nil

	case "setPrefs":
		sv.mu.Lock()
		sv.prefs = p.Prefs
		sv.mu.Unlock()
		sv.saveRules()
		return nil, nil

	case "usage":
		return sv.usage.report(), nil

	case "resetUsage":
		sv.usage.reset()
		for _, a := range sv.S.Apps() {
			a.SetUsed(0)
		}
		return sv.usage.report(), nil

	case "reveal":
		if a := sv.S.Lookup(p.ID); a != nil && a.Path != "" {
			return nil, revealFile(a.Path)
		}
		return nil, nil

	case "useDemo":
		if err := sv.UseEngine(engine.NewDemo(sv.S, sv.Opts)); err != nil {
			return nil, err
		}
		return sv.Snapshot(), nil

	case "log":
		return sv.logSince(p.Since), nil

	case "decide":
		if err := sv.decide(p.ID, p.Allow); err != nil {
			return nil, err
		}
		return sv.Snapshot(), nil

	case "setAsk":
		sv.setAsk(p.On)
		return sv.Snapshot(), nil

	case "setSplit":
		if sv.S.Lookup(p.ID) == nil {
			return nil, errors.New("unknown app")
		}
		sv.setSplit(p.ID, p.On)
		return sv.Snapshot(), nil

	case "sitePresets":
		return sites.Presets, nil

	case "setSite":
		if p.Site == nil || p.Rule == nil {
			return nil, errors.New("missing website rule")
		}
		if err := sv.setSite(*p.Site, *p.Rule); err != nil {
			return nil, err
		}
		return sv.Snapshot(), nil

	case "deleteSite":
		sv.deleteSite(p.ID)
		return sv.Snapshot(), nil
	}
	if h := sv.Hooks[method]; h != nil {
		return h(params)
	}
	return nil, fmt.Errorf("unknown method %q", method)
}
