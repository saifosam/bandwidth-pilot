package sites

import (
	"net/netip"
	"testing"
)

// dnsAnswer builds a response for qname with a CNAME then the given A records,
// using name compression like real resolvers do.
func dnsAnswer(qname string, ips ...[4]byte) []byte {
	m := []byte{0x12, 0x34, 0x81, 0x80, 0, 1, 0, byte(1 + len(ips)), 0, 0, 0, 0}
	enc := func(n string) []byte {
		var b []byte
		start := 0
		for i := 0; i <= len(n); i++ {
			if i == len(n) || n[i] == '.' {
				b = append(b, byte(i-start))
				b = append(b, n[start:i]...)
				start = i + 1
			}
		}
		return append(b, 0)
	}
	m = append(m, enc(qname)...)
	m = append(m, 0, 1, 0, 1)
	// CNAME answer pointing at the question name (offset 12)
	cname := enc("edge.cdn.example.net")
	m = append(m, 0xC0, 12, 0, 5, 0, 1, 0, 0, 0, 60, 0, byte(len(cname)))
	cnameOff := len(m)
	m = append(m, cname...)
	for _, ip := range ips {
		m = append(m, 0xC0|byte(cnameOff>>8), byte(cnameOff), 0, 1, 0, 1, 0, 0, 0, 60, 0, 4)
		m = append(m, ip[:]...)
	}
	return m
}

func TestParseDNSResponse(t *testing.T) {
	q, addrs := ParseDNSResponse(dnsAnswer("www.YouTube.com", [4]byte{142, 250, 1, 1}, [4]byte{142, 250, 1, 2}))
	if q != "www.youtube.com" || len(addrs) != 2 || addrs[1].String() != "142.250.1.2" {
		t.Fatalf("got %q %v", q, addrs)
	}
	if q, a := ParseDNSResponse([]byte{1, 2, 3}); q != "" || a != nil {
		t.Fatal("short message must be ignored")
	}
	query := dnsAnswer("x.com", [4]byte{1, 1, 1, 1})
	query[2] = 0x01 // a query, not a response
	if _, a := ParseDNSResponse(query); a != nil {
		t.Fatal("queries must be ignored")
	}
	// Truncated in the middle of an answer: keep what parsed, do not panic.
	full := dnsAnswer("a.example.com", [4]byte{9, 9, 9, 9}, [4]byte{8, 8, 8, 8})
	for i := 12; i < len(full); i++ {
		ParseDNSResponse(full[:i])
	}
	// Pointer loop must not hang.
	loop := []byte{0, 0, 0x81, 0x80, 0, 1, 0, 1, 0, 0, 0, 0, 0xC0, 12}
	ParseDNSResponse(loop)
}

func TestNormalize(t *testing.T) {
	ok := map[string]string{
		"YouTube.com":                    "youtube.com",
		"https://www.netflix.com/browse": "www.netflix.com",
		"*.googlevideo.com":              "googlevideo.com",
		"example.org:8080":               "example.org",
		"203.0.113.7":                    "203.0.113.7",
		"203.0.113.77/24":                "203.0.113.0/24",
		"2001:db8::1":                    "2001:db8::1",
	}
	for in, want := range ok {
		if got, err := Normalize(in); err != nil || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "localhost", "hello world", "a,b.com"} {
		if _, err := Normalize(bad); err == nil {
			t.Errorf("Normalize(%q) should fail", bad)
		}
	}
}

func TestMatch(t *testing.T) {
	tb := New()
	tb.Set([]Site{
		{ID: "yt", Name: "YouTube", Patterns: []string{"youtube.com", "googlevideo.com"}},
		{ID: "lan", Name: "Office", Patterns: []string{"10.20.0.0/16", "192.0.2.5"}},
	}, func(s Site) any { return s.ID })
	gen := tb.Gen()

	vid := netip.MustParseAddr("142.250.1.1")
	if tb.Match(vid) != nil {
		t.Fatal("unknown address must not match")
	}
	tb.Learn(dnsAnswer("rr3---sn-abc.googlevideo.com", [4]byte{142, 250, 1, 1}))
	if tb.Host(vid) != "rr3---sn-abc.googlevideo.com" || tb.Match(vid) != "yt" {
		t.Fatalf("subdomain should match: host=%q", tb.Host(vid))
	}
	tb.Remember(netip.MustParseAddr("1.2.3.4"), "notyoutube.com")
	if tb.Match(netip.MustParseAddr("1.2.3.4")) != nil {
		t.Fatal("suffix match must respect label boundaries")
	}
	if tb.Match(netip.MustParseAddr("10.20.99.1")) != "lan" || tb.Match(netip.MustParseAddr("192.0.2.5")) != "lan" {
		t.Fatal("IP and range rules must match")
	}
	if tb.Match(netip.MustParseAddr("::ffff:10.20.1.1")) != "lan" {
		t.Fatal("IPv4-mapped addresses must match IPv4 rules")
	}
	tb.Set(nil, func(Site) any { return nil })
	if tb.Gen() == gen || tb.Match(vid) != nil {
		t.Fatal("clearing rules must bump the generation and stop matching")
	}
}
