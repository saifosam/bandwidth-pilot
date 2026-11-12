package core

import (
	"encoding/json"
	"net/netip"
	"testing"

	"bandwidthpilot/internal/engine"
	"bandwidthpilot/internal/shaper"
	"bandwidthpilot/internal/sites"
)

func TestAskNewApps(t *testing.T) {
	dir := t.TempDir()
	sv := New(dir, "test", nil)
	old := sv.S.App(`c:\old.exe`, "Old", `C:\old.exe`)
	sv.setAsk(true)
	if old.Pending() {
		t.Fatal("apps seen before asking was switched on must not be held")
	}
	fresh := sv.S.App(`c:\new.exe`, "New", `C:\new.exe`)
	win := sv.S.App(`c:\windows\system32\x.exe`, "X", `C:\Windows\System32\x.exe`)
	if !fresh.Pending() || win.Pending() {
		t.Fatalf("pending: fresh=%v windows=%v", fresh.Pending(), win.Pending())
	}
	if len(sv.Snapshot().Pending) != 1 || !sv.S.NeedsIntercept() {
		t.Fatal("pending app should be listed and held")
	}
	if sv.S.Admit(fresh, shaper.Out, 100) != shaper.Drop {
		t.Fatal("pending app traffic must be held back")
	}
	sv.addLog(engine.ConnEvent{AppID: fresh.ID, Remote: "203.0.113.5", Host: "cdn.example.com", Port: 443})
	if p := sv.Snapshot().Pending[0]; p.Host != "cdn.example.com" {
		t.Fatalf("pending should show where it tried to go: %+v", p)
	}
	if err := sv.decide(fresh.ID, false); err != nil {
		t.Fatal(err)
	}
	if fresh.Pending() || !fresh.Rule().BlockIn || !fresh.Rule().BlockOut {
		t.Fatal("block decision should leave a block rule")
	}

	// Restart: the decision and the setting survive.
	sv.Close()
	sv2 := New(dir, "test", nil)
	if !sv2.Snapshot().Ask {
		t.Fatal("ask setting not saved")
	}
	again := sv2.S.App(`c:\new.exe`, "New", `C:\new.exe`)
	if again.Pending() || !again.Rule().BlockOut {
		t.Fatal("known app must not be asked about again and keeps its block")
	}
	other := sv2.S.App(`c:\other.exe`, "Other", `C:\other.exe`)
	if !other.Pending() {
		t.Fatal("unknown app after restart must be asked about")
	}
	sv2.setAsk(false)
	if other.Pending() {
		t.Fatal("switching asking off releases waiting apps")
	}
}

func TestWebsiteRules(t *testing.T) {
	dir := t.TempDir()
	sv := New(dir, "test", nil)
	rule := shaper.Rule{Priority: shaper.PrioNormal, LimitIn: shaper.Limit{On: true, Bps: 100_000}}
	if err := sv.setSite(sites.Site{Name: "YouTube", Patterns: []string{"youtube.com, https://googlevideo.com/x"}}, rule); err != nil {
		t.Fatal(err)
	}
	if err := sv.setSite(sites.Site{Patterns: []string{"not a site"}}, rule); err == nil {
		t.Fatal("bad pattern should be rejected")
	}
	snap := sv.Snapshot()
	if len(snap.Sites) != 1 || len(snap.Sites[0].Patterns) != 2 || snap.Sites[0].Patterns[1] != "googlevideo.com" {
		t.Fatalf("sites: %+v", snap.Sites)
	}
	for _, a := range snap.Apps {
		if a.ID[:5] == "site:" {
			t.Fatal("website rules must not appear as apps")
		}
	}
	ip := netip.MustParseAddr("142.250.1.1")
	sv.Sites.Remember(ip, "rr1.googlevideo.com")
	m, _ := sv.Sites.Match(ip).(*shaper.App)
	if m == nil || m.Rule().LimitIn.Bps != 100_000 {
		t.Fatal("website rule should match and carry its limit")
	}
	id := snap.Sites[0].ID
	sv.Close()

	sv2 := New(dir, "test", nil)
	sv2.Sites.Remember(ip, "rr1.googlevideo.com")
	if m, _ := sv2.Sites.Match(ip).(*shaper.App); m == nil || !m.Rule().LimitIn.On {
		t.Fatal("website rule not restored")
	}
	sv2.deleteSite(id)
	if sv2.Sites.Match(ip) != nil || len(sv2.Snapshot().Sites) != 0 {
		t.Fatal("deleted website rule still active")
	}
}

func TestSplitAndRPC(t *testing.T) {
	sv := New(t.TempDir(), "test", nil)
	parent := sv.S.App(`c:\chrome.exe`, "Chrome", `C:\chrome.exe`)
	sv.setSplit(parent.ID, true)
	if !sv.isSplit(parent.ID) {
		t.Fatal("split not recorded")
	}
	child := sv.Opts.Split(parent.ID)
	if !child {
		t.Fatal("engine option must see the split")
	}
	sv.S.AppWithParent("pid:42:"+parent.ID, "Chrome (PID 42)", parent.Path, parent.ID)
	sv.setSplit(parent.ID, false)
	if sv.S.Lookup("pid:42:"+parent.ID) != nil {
		t.Fatal("merging back must remove process rows")
	}
	raw, _ := json.Marshal(map[string]any{"since": 0})
	if _, err := sv.Call("log", raw); err != nil {
		t.Fatal(err)
	}
	sv.Hooks["ping"] = func(json.RawMessage) (any, error) { return "pong", nil }
	if r, err := sv.Call("ping", nil); err != nil || r != "pong" {
		t.Fatal("hooks must be reachable")
	}
}
