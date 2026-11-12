package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"bandwidthpilot/internal/engine"
	"bandwidthpilot/internal/shaper"
	"bandwidthpilot/internal/sites"
)

// features holds the state behind the connection log, new-app prompts,
// per-process splitting and website rules. It has its own lock because the
// engines call into it from the packet path.
type features struct {
	mu      sync.Mutex
	ask     bool
	known   map[string]bool
	dirty   bool
	pending map[string]*Pending
	log     []LogEntry
	logSeq  int64
	split   map[string]bool
	sites   []sites.Site
}

// Pending is an app waiting for the user to allow or block it.
type Pending struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	Since  int64  `json:"since"` // unix ms
	Host   string `json:"host"`
	Remote string `json:"remote"`
	Port   uint16 `json:"port"`
}

// LogEntry is one connection in the log.
type LogEntry struct {
	Seq int64 `json:"seq"`
	engine.ConnEvent
}

// SiteView is one website rule as shown in the UI.
type SiteView struct {
	sites.Site
	Rule     shaper.Rule `json:"rule"`
	InForce  bool        `json:"inForce"`
	RateIn   int64       `json:"rateIn"`
	RateOut  int64       `json:"rateOut"`
	TotalIn  int64       `json:"totalIn"`
	TotalOut int64       `json:"totalOut"`
	Dropped  int64       `json:"dropped"`
}

const (
	logCap     = 1500
	sitePrefix = "site:"
)

func newFeatures() features {
	return features{known: map[string]bool{}, pending: map[string]*Pending{}, split: map[string]bool{}}
}

func (sv *Service) knownPath() string { return filepath.Join(sv.dir, "known-apps.json") }

func (sv *Service) loadKnown() {
	b, err := os.ReadFile(sv.knownPath())
	if err != nil {
		return
	}
	var ids []string
	if json.Unmarshal(b, &ids) == nil {
		for _, id := range ids {
			sv.f.known[id] = true
		}
	}
}

func (sv *Service) saveKnown() {
	sv.f.mu.Lock()
	if !sv.f.dirty {
		sv.f.mu.Unlock()
		return
	}
	ids := make([]string, 0, len(sv.f.known))
	for id := range sv.f.known {
		ids = append(ids, id)
	}
	sv.f.dirty = false
	sv.f.mu.Unlock()
	sort.Strings(ids)
	writeJSON(sv.knownPath(), ids)
}

// exempt reports whether an app never needs approval: website rules,
// per-process rows, and parts of Windows itself.
func exempt(a *shaper.App) bool {
	if strings.HasPrefix(a.ID, sitePrefix) || a.Parent != "" || a.ID == "system" || a.ID == "unknown" {
		return true
	}
	p := strings.ToLower(a.Path)
	return strings.HasPrefix(p, `c:\windows\`) || strings.Contains(p, `\windows defender\`)
}

// onNewApp runs the first time any app is seen.
func (sv *Service) onNewApp(a *shaper.App) {
	if exempt(a) {
		return
	}
	sv.f.mu.Lock()
	if sv.f.known[a.ID] {
		sv.f.mu.Unlock()
		return
	}
	if !sv.f.ask {
		sv.f.known[a.ID] = true
		sv.f.dirty = true
		sv.f.mu.Unlock()
		return
	}
	sv.f.pending[a.ID] = &Pending{ID: a.ID, Name: a.Name, Path: a.Path, Since: time.Now().UnixMilli()}
	sv.f.mu.Unlock()
	sv.S.SetPending(a, true)
	sv.logf("new app waiting for approval: %s", a.Path)
	if sv.OnPending != nil {
		go sv.OnPending(a.Name)
	}
}

// addLog records a new connection.
func (sv *Service) addLog(ev engine.ConnEvent) {
	sv.f.mu.Lock()
	defer sv.f.mu.Unlock()
	sv.f.logSeq++
	sv.f.log = append(sv.f.log, LogEntry{Seq: sv.f.logSeq, ConnEvent: ev})
	if len(sv.f.log) > logCap {
		sv.f.log = append(sv.f.log[:0], sv.f.log[len(sv.f.log)-logCap:]...)
	}
	if p := sv.f.pending[ev.AppID]; p != nil && p.Remote == "" {
		p.Host, p.Remote, p.Port = ev.Host, ev.Remote, ev.Port
	}
}

func (sv *Service) logSince(seq int64) []LogEntry {
	sv.f.mu.Lock()
	defer sv.f.mu.Unlock()
	i := sort.Search(len(sv.f.log), func(i int) bool { return sv.f.log[i].Seq > seq })
	return append([]LogEntry{}, sv.f.log[i:]...)
}

func (sv *Service) pendingList() []Pending {
	sv.f.mu.Lock()
	defer sv.f.mu.Unlock()
	out := make([]Pending, 0, len(sv.f.pending))
	for _, p := range sv.f.pending {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since < out[j].Since })
	return out
}

// decide answers a new-app prompt.
func (sv *Service) decide(id string, allow bool) error {
	a := sv.S.Lookup(id)
	if a == nil {
		return errors.New("unknown app")
	}
	sv.f.mu.Lock()
	delete(sv.f.pending, id)
	sv.f.known[id] = true
	sv.f.dirty = true
	sv.f.mu.Unlock()
	if !allow {
		r := a.Rule()
		r.BlockIn, r.BlockOut = true, true
		sv.S.SetRule(a, r)
	}
	sv.S.SetPending(a, false)
	sv.saveRules()
	sv.saveKnown()
	return nil
}

// setAsk turns new-app prompts on or off.
func (sv *Service) setAsk(on bool) {
	var release []string
	sv.f.mu.Lock()
	sv.f.ask = on
	if on {
		// Everything already seen counts as known; only newcomers are asked about.
		for _, a := range sv.S.Apps() {
			if !exempt(a) {
				sv.f.known[a.ID] = true
			}
		}
		sv.f.dirty = true
	} else {
		for id := range sv.f.pending {
			release = append(release, id)
		}
	}
	sv.f.mu.Unlock()
	for _, id := range release {
		sv.decide(id, true)
	}
	sv.saveRules()
	sv.saveKnown()
}

func (sv *Service) isSplit(id string) bool {
	sv.f.mu.Lock()
	defer sv.f.mu.Unlock()
	return sv.f.split[id]
}

// setSplit shows an app as one row per process, or merges it back.
func (sv *Service) setSplit(id string, on bool) {
	sv.f.mu.Lock()
	if on {
		sv.f.split[id] = true
	} else {
		delete(sv.f.split, id)
	}
	sv.f.mu.Unlock()
	if e := sv.engine(); e != nil {
		e.Resplit()
	}
	if !on {
		for _, a := range sv.S.Apps() {
			if a.Parent == id {
				sv.S.Remove(a.ID)
			}
		}
	}
	sv.saveRules()
}

func (sv *Service) siteApp(s sites.Site) *shaper.App {
	return sv.S.App(sitePrefix+s.ID, s.Name, "")
}

func (sv *Service) applySites() {
	sv.f.mu.Lock()
	list := append([]sites.Site(nil), sv.f.sites...)
	sv.f.mu.Unlock()
	sv.Sites.Set(list, func(s sites.Site) any { return sv.siteApp(s) })
}

// setSite adds or updates a website rule.
func (sv *Service) setSite(in sites.Site, r shaper.Rule) error {
	name := strings.TrimSpace(in.Name)
	var pats []string
	seen := map[string]bool{}
	for _, raw := range in.Patterns {
		for _, part := range strings.FieldsFunc(raw, func(c rune) bool { return c == ',' || c == ' ' || c == '\n' || c == ';' }) {
			p, err := sites.Normalize(part)
			if err != nil {
				return err
			}
			if !seen[p] {
				seen[p] = true
				pats = append(pats, p)
			}
		}
	}
	if len(pats) == 0 {
		return errors.New("add at least one website, IP address or range")
	}
	if name == "" {
		name = pats[0]
	}
	sv.f.mu.Lock()
	id := in.ID
	if id == "" {
		id = fmt.Sprintf("%x", time.Now().UnixNano())
	}
	s := sites.Site{ID: id, Name: name, Patterns: pats}
	replaced := false
	for i := range sv.f.sites {
		if sv.f.sites[i].ID == id {
			sv.f.sites[i] = s
			replaced = true
		}
	}
	if !replaced {
		sv.f.sites = append(sv.f.sites, s)
	}
	sv.f.mu.Unlock()
	a := sv.siteApp(s)
	a.Name = name
	if r.Priority < shaper.PrioLow || r.Priority > shaper.PrioCritical {
		r.Priority = shaper.PrioNormal
	}
	r.Quota = shaper.Quota{}
	sv.S.SetRule(a, r)
	sv.applySites()
	sv.saveRules()
	return nil
}

func (sv *Service) deleteSite(id string) {
	sv.f.mu.Lock()
	out := sv.f.sites[:0]
	for _, s := range sv.f.sites {
		if s.ID != id {
			out = append(out, s)
		}
	}
	sv.f.sites = out
	sv.f.mu.Unlock()
	if a := sv.S.Lookup(sitePrefix + id); a != nil {
		sv.S.ClearRule(a)
		sv.S.Remove(a.ID)
	}
	sv.applySites()
	sv.saveRules()
}

// siteViews builds the Websites tab. Called with sv.mu held.
func (sv *Service) siteViews() []SiteView {
	sv.f.mu.Lock()
	list := append([]sites.Site(nil), sv.f.sites...)
	sv.f.mu.Unlock()
	out := make([]SiteView, 0, len(list))
	for _, s := range list {
		v := SiteView{Site: s}
		if a := sv.S.Lookup(sitePrefix + s.ID); a != nil {
			v.Rule, v.InForce = a.Rule(), a.InForce()
			v.TotalIn, v.TotalOut = a.Passed(shaper.In), a.Passed(shaper.Out)
			v.Dropped = a.Dropped(shaper.In) + a.Dropped(shaper.Out)
			if st := sv.stats[a.ID]; st != nil {
				v.RateIn, v.RateOut = st.rate[0], st.rate[1]
			}
		}
		out = append(out, v)
	}
	return out
}
