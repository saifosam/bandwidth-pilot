// Package sites matches traffic to websites so it can be blocked or limited
// regardless of which app sends it.
//
// Most connections start with a DNS lookup. The engine passes every DNS
// answer it sees to Learn, which records which name each IP address was
// looked up as. A site rule lists names ("youtube.com" also matches
// "www.youtube.com"), IP addresses or ranges ("203.0.113.0/24"), and Match
// tells the engine which rule, if any, a remote address belongs to.
package sites

import (
	"errors"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Site is one website rule as stored and shown in the UI. The speed limit or
// block itself lives in a shaper app with the id "site:" + ID.
type Site struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Patterns []string `json:"patterns"`
}

type compiled struct {
	site     Site
	domains  []string
	prefixes []netip.Prefix
	payload  any
}

type nameEntry struct {
	name string
	at   int64 // unix seconds
}

// Table holds the site rules and the DNS cache.
type Table struct {
	mu    sync.RWMutex
	sites []compiled
	gen   atomic.Int64

	dnsMu sync.RWMutex
	names map[netip.Addr]nameEntry
}

// New returns an empty table.
func New() *Table {
	t := &Table{names: map[netip.Addr]nameEntry{}}
	t.gen.Store(1)
	return t
}

// Gen changes every time the rules change, so cached matches can be redone.
func (t *Table) Gen() int64 { return t.gen.Load() }

// Normalize cleans up one pattern typed by the user. It accepts a domain,
// a URL, an IP address or a CIDR range.
func Normalize(p string) (string, error) {
	p = strings.ToLower(strings.TrimSpace(p))
	if p == "" {
		return "", errors.New("empty pattern")
	}
	if pf, err := netip.ParsePrefix(p); err == nil {
		return pf.Masked().String(), nil
	}
	if ip, err := netip.ParseAddr(p); err == nil {
		return ip.Unmap().String(), nil
	}
	if i := strings.Index(p, "://"); i >= 0 {
		p = p[i+3:]
	}
	if i := strings.IndexAny(p, "/?#"); i >= 0 {
		p = p[:i]
	}
	if i := strings.LastIndex(p, ":"); i >= 0 {
		p = p[:i]
	}
	p = strings.TrimPrefix(p, "*.")
	p = strings.Trim(p, ".")
	if p == "" || strings.ContainsAny(p, " \t,;*") || !strings.Contains(p, ".") {
		return "", errors.New("not a website, IP address or range: " + p)
	}
	return p, nil
}

// Set replaces the rules. payload(site) supplies what Match returns for it.
func (t *Table) Set(list []Site, payload func(Site) any) {
	var out []compiled
	for _, s := range list {
		c := compiled{site: s, payload: payload(s)}
		for _, p := range s.Patterns {
			if pf, err := netip.ParsePrefix(p); err == nil {
				c.prefixes = append(c.prefixes, pf)
			} else if ip, err := netip.ParseAddr(p); err == nil {
				c.prefixes = append(c.prefixes, netip.PrefixFrom(ip, ip.BitLen()))
			} else {
				c.domains = append(c.domains, p)
			}
		}
		out = append(out, c)
	}
	t.mu.Lock()
	t.sites = out
	t.mu.Unlock()
	t.gen.Add(1)
}

// Host returns the name an address was looked up as, or "".
func (t *Table) Host(ip netip.Addr) string {
	t.dnsMu.RLock()
	defer t.dnsMu.RUnlock()
	return t.names[ip.Unmap()].name
}

// Match returns the payload of the first rule that covers the address
// (checked by IP and by the name it was looked up as), or nil.
func (t *Table) Match(ip netip.Addr) any {
	ip = ip.Unmap()
	host := t.Host(ip)
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, c := range t.sites {
		for _, pf := range c.prefixes {
			if pf.Contains(ip) {
				return c.payload
			}
		}
		if host != "" {
			for _, d := range c.domains {
				if host == d || strings.HasSuffix(host, "."+d) {
					return c.payload
				}
			}
		}
	}
	return nil
}

// Remember records that ip was looked up as name.
func (t *Table) Remember(ip netip.Addr, name string) {
	now := time.Now().Unix()
	t.dnsMu.Lock()
	defer t.dnsMu.Unlock()
	if len(t.names) > 50_000 {
		// Drop the oldest half.
		ages := make([]int64, 0, len(t.names))
		for _, e := range t.names {
			ages = append(ages, e.at)
		}
		sort.Slice(ages, func(i, j int) bool { return ages[i] < ages[j] })
		cut := ages[len(ages)/2]
		for k, e := range t.names {
			if e.at <= cut {
				delete(t.names, k)
			}
		}
	}
	t.names[ip.Unmap()] = nameEntry{name: name, at: now}
}

// Learn parses a DNS response (the UDP payload) and remembers every address
// in it under the name that was asked for.
func (t *Table) Learn(msg []byte) {
	q, addrs := ParseDNSResponse(msg)
	for _, a := range addrs {
		t.Remember(a, q)
	}
}

// ParseDNSResponse returns the question name and the A/AAAA addresses of a
// DNS response. CNAME chains are followed implicitly: every address in the
// answer belongs to the name the client asked for.
func ParseDNSResponse(m []byte) (string, []netip.Addr) {
	if len(m) < 12 || m[2]&0x80 == 0 { // not a response
		return "", nil
	}
	qd := int(m[4])<<8 | int(m[5])
	an := int(m[6])<<8 | int(m[7])
	if qd != 1 || an == 0 || an > 64 {
		return "", nil
	}
	name, off, ok := readName(m, 12)
	if !ok || off+4 > len(m) {
		return "", nil
	}
	off += 4 // qtype, qclass
	var out []netip.Addr
	for i := 0; i < an; i++ {
		_, n, ok := readName(m, off)
		if !ok || n+10 > len(m) {
			break
		}
		typ := int(m[n])<<8 | int(m[n+1])
		rdlen := int(m[n+8])<<8 | int(m[n+9])
		data := n + 10
		if data+rdlen > len(m) {
			break
		}
		switch {
		case typ == 1 && rdlen == 4:
			out = append(out, netip.AddrFrom4([4]byte(m[data:data+4])))
		case typ == 28 && rdlen == 16:
			out = append(out, netip.AddrFrom16([16]byte(m[data:data+16])))
		}
		off = data + rdlen
	}
	return strings.ToLower(name), out
}

// readName decodes a possibly compressed name at off. It returns the name
// and the offset just after the name in the original position.
func readName(m []byte, off int) (string, int, bool) {
	var parts []string
	end := -1
	for jumps := 0; jumps < 16; {
		if off >= len(m) {
			return "", 0, false
		}
		l := int(m[off])
		switch {
		case l == 0:
			if end < 0 {
				end = off + 1
			}
			return strings.Join(parts, "."), end, true
		case l&0xC0 == 0xC0:
			if off+1 >= len(m) {
				return "", 0, false
			}
			if end < 0 {
				end = off + 2
			}
			off = (l&0x3F)<<8 | int(m[off+1])
			jumps++
		case l < 64:
			if off+1+l > len(m) {
				return "", 0, false
			}
			parts = append(parts, string(m[off+1:off+1+l]))
			off += 1 + l
		default:
			return "", 0, false
		}
	}
	return "", 0, false
}

// Preset is a ready-made rule offered in the UI.
type Preset struct {
	Name     string   `json:"name"`
	Patterns []string `json:"patterns"`
}

// Presets are common sites people want to slow down or block. Video sites
// stream from separate CDN domains, which are included.
var Presets = []Preset{
	{"YouTube", []string{"youtube.com", "googlevideo.com", "ytimg.com", "youtu.be", "youtube-nocookie.com"}},
	{"Netflix", []string{"netflix.com", "nflxvideo.net", "nflximg.net", "nflxext.com", "nflxso.net"}},
	{"TikTok", []string{"tiktok.com", "tiktokcdn.com", "tiktokv.com", "byteoversea.com", "ibytedtos.com"}},
	{"Instagram & Facebook", []string{"instagram.com", "cdninstagram.com", "facebook.com", "fbcdn.net", "fbsbx.com"}},
	{"Twitch", []string{"twitch.tv", "ttvnw.net", "jtvnw.net"}},
	{"Steam downloads", []string{"steamcontent.com", "steamserver.net", "steampowered.com"}},
	{"Windows Update", []string{"windowsupdate.com", "update.microsoft.com", "delivery.mp.microsoft.com", "dl.delivery.mp.microsoft.com"}},
}
