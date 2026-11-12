package engine

import "testing"

func TestParseIPv4TCP(t *testing.T) {
	b := make([]byte, 40)
	b[0] = 0x45
	b[9] = 6
	copy(b[12:], []byte{192, 168, 1, 20})
	copy(b[16:], []byte{203, 0, 113, 9})
	b[20], b[21] = 0xC0, 0x01 // 49153
	b[22], b[23] = 0x01, 0xBB // 443
	p, ok := parsePacket(b)
	if !ok || p.proto != 6 || p.sport != 49153 || p.dport != 443 ||
		p.src.String() != "192.168.1.20" || p.dst.String() != "203.0.113.9" {
		t.Fatalf("bad parse: %+v ok=%v", p, ok)
	}
}

func TestParseIPv4Options(t *testing.T) {
	b := make([]byte, 48)
	b[0] = 0x46 // 24-byte header
	b[9] = 17
	b[24], b[25] = 0x00, 0x35
	b[26], b[27] = 0xD4, 0x31
	p, ok := parsePacket(b)
	if !ok || p.proto != 17 || p.sport != 53 || p.dport != 54321 {
		t.Fatalf("bad parse: %+v ok=%v", p, ok)
	}
}

func TestParseIPv4Fragment(t *testing.T) {
	b := make([]byte, 40)
	b[0] = 0x45
	b[9] = 17
	b[6], b[7] = 0x00, 0xB9 // fragment offset != 0
	if _, ok := parsePacket(b); ok {
		t.Fatal("non-first fragment should not parse")
	}
}

func TestParseIPv6(t *testing.T) {
	b := make([]byte, 60)
	b[0] = 0x60
	b[6] = 6
	b[8] = 0x20
	b[9] = 0x01
	b[39] = 1
	b[40], b[41] = 0x1F, 0x90 // 8080
	b[42], b[43] = 0x00, 0x50 // 80
	p, ok := parsePacket(b)
	if !ok || p.proto != 6 || p.sport != 8080 || p.dport != 80 || p.dst.String() != "::1" {
		t.Fatalf("bad parse: %+v ok=%v", p, ok)
	}
}

func TestParseIPv6ExtensionHeader(t *testing.T) {
	b := make([]byte, 68)
	b[0] = 0x60
	b[6] = 0   // hop-by-hop
	b[40] = 17 // next: UDP
	b[41] = 0  // 8 bytes long
	b[48], b[49] = 0x01, 0xBB
	b[50], b[51] = 0xC3, 0x50
	p, ok := parsePacket(b)
	if !ok || p.proto != 17 || p.sport != 443 || p.dport != 50000 {
		t.Fatalf("bad parse: %+v ok=%v", p, ok)
	}
}

func TestParseRejectsOther(t *testing.T) {
	b := make([]byte, 40)
	b[0] = 0x45
	b[9] = 1 // ICMP
	if _, ok := parsePacket(b); ok {
		t.Fatal("ICMP should not parse")
	}
	if _, ok := parsePacket([]byte{0x45, 0}); ok {
		t.Fatal("short packet should not parse")
	}
}
