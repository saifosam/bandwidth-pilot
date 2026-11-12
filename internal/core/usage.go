package core

import (
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"sync"
	"time"
)

// usageStore keeps per-app, per-day byte counts for the Usage tab and for
// daily quotas. It is a small JSON file; 31 days are kept.
type usageStore struct {
	mu    sync.Mutex
	path  string
	day   string
	Days  map[string]map[string][2]int64 `json:"days"` // date -> app id -> [in, out]
	Names map[string]string              `json:"names"`
}

func loadUsage(path string) *usageStore {
	u := &usageStore{path: path}
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, u)
	}
	if u.Days == nil {
		u.Days = map[string]map[string][2]int64{}
	}
	if u.Names == nil {
		u.Names = map[string]string{}
	}
	u.day = time.Now().Format("2006-01-02")
	return u
}

// roll moves to a new day; it reports whether the date changed.
func (u *usageStore) roll(day string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if day == u.day {
		return false
	}
	u.day = day
	return true
}

func (u *usageStore) today(id string) (in, out int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	v := u.Days[u.day][id]
	return v[0], v[1]
}

func (u *usageStore) add(id, name string, in, out int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	d := u.Days[u.day]
	if d == nil {
		d = map[string][2]int64{}
		u.Days[u.day] = d
	}
	v := d[id]
	v[0] += in
	v[1] += out
	d[id] = v
	u.Names[id] = name
}

func (u *usageStore) save() {
	u.mu.Lock()
	cutoff := time.Now().AddDate(0, 0, -31).Format("2006-01-02")
	for day := range u.Days {
		if day < cutoff {
			delete(u.Days, day)
		}
	}
	live := map[string]bool{}
	for _, d := range u.Days {
		for id := range d {
			live[id] = true
		}
	}
	for id := range u.Names {
		if !live[id] {
			delete(u.Names, id)
		}
	}
	b, err := json.Marshal(u)
	u.mu.Unlock()
	if err != nil {
		return
	}
	tmp := u.path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, u.path)
	}
}

func (u *usageStore) reset() {
	u.mu.Lock()
	u.Days = map[string]map[string][2]int64{}
	u.Names = map[string]string{}
	u.mu.Unlock()
	u.save()
}

// UsageApp is one app's totals in the usage report.
type UsageApp struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Today [2]int64 `json:"today"`
	Week  [2]int64 `json:"week"`
	Month [2]int64 `json:"month"`
}

// UsageDay is one day's total in the usage report.
type UsageDay struct {
	Date string `json:"date"`
	In   int64  `json:"in"`
	Out  int64  `json:"out"`
}

// UsageReport feeds the Usage tab.
type UsageReport struct {
	Today string     `json:"today"`
	Days  []UsageDay `json:"days"` // the last 30 days, oldest first
	Apps  []UsageApp `json:"apps"`
}

func (u *usageStore) report() UsageReport {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := time.Now()
	rep := UsageReport{Today: u.day}
	week := now.AddDate(0, 0, -6).Format("2006-01-02")
	apps := map[string]*UsageApp{}
	for i := 29; i >= 0; i-- {
		date := now.AddDate(0, 0, -i).Format("2006-01-02")
		day := UsageDay{Date: date}
		for id, v := range u.Days[date] {
			day.In += v[0]
			day.Out += v[1]
			a := apps[id]
			if a == nil {
				a = &UsageApp{ID: id, Name: u.Names[id]}
				apps[id] = a
			}
			a.Month[0] += v[0]
			a.Month[1] += v[1]
			if date >= week {
				a.Week[0] += v[0]
				a.Week[1] += v[1]
			}
			if date == u.day {
				a.Today = v
			}
		}
		rep.Days = append(rep.Days, day)
	}
	for _, a := range apps {
		rep.Apps = append(rep.Apps, *a)
	}
	sort.Slice(rep.Apps, func(i, j int) bool {
		ti, tj := rep.Apps[i].Month[0]+rep.Apps[i].Month[1], rep.Apps[j].Month[0]+rep.Apps[j].Month[1]
		if ti != tj {
			return ti > tj
		}
		return rep.Apps[i].Name < rep.Apps[j].Name
	})
	if rep.Apps == nil {
		rep.Apps = []UsageApp{}
	}
	return rep
}

// revealFile shows a file in the system file manager.
func revealFile(path string) error {
	switch runtime.GOOS {
	case "windows":
		// explorer.exe exits non-zero even on success, so only Start it.
		return exec.Command("explorer.exe", "/select,"+path).Start()
	case "darwin":
		return exec.Command("open", "-R", path).Start()
	}
	return nil
}
