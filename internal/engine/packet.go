package engine

import "net/netip"

// pktInfo is the part of an IP packet the engine cares about.
type pktInfo struct {
	proto uint8
	src   netip.Addr
	dst   netip.Addr
	sport uint16
	dport uint16
}

// parsePacket reads the addresses, protocol and ports from a raw IPv4 or
// IPv6 packet. ok is false for anything that is not TCP or UDP with a
// visible transport header (for example non-first fragments).
func parsePacket(b []byte) (p pktInfo, ok bool) {
	if len(b) < 20 {
		return p, false
	}
	var l4 []byte
	switch b[0] >> 4 {
	case 4:
		ihl := int(b[0]&0x0f) * 4
		if ihl < 20 || len(b) < ihl+4 {
			return p, false
		}
		if (uint16(b[6])<<8|uint16(b[7]))&0x1fff != 0 {
			return p, false // fragment without a transport header
		}
		p.proto = b[9]
		p.src = netip.AddrFrom4([4]byte(b[12:16]))
		p.dst = netip.AddrFrom4([4]byte(b[16:20]))
		l4 = b[ihl:]
	case 6:
		if len(b) < 40 {
			return p, false
		}
		next, off := b[6], 40
		// Skip extension headers until we reach the transport header.
		for i := 0; i < 8; i++ {
			if next != 0 && next != 43 && next != 60 && next != 44 {
				break
			}
			if len(b) < off+8 {
				return p, false
			}
			if next == 44 { // fragment header: fixed 8 bytes
				if (uint16(b[off+2])<<8|uint16(b[off+3]))&0xfff8 != 0 {
					return p, false
				}
				next, off = b[off], off+8
				continue
			}
			next, off = b[off], off+(int(b[off+1])+1)*8
		}
		if len(b) < off+4 {
			return p, false
		}
		p.proto = next
		p.src = netip.AddrFrom16([16]byte(b[8:24]))
		p.dst = netip.AddrFrom16([16]byte(b[24:40]))
		l4 = b[off:]
	default:
		return p, false
	}
	if p.proto != protoTCP && p.proto != protoUDP {
		return p, false
	}
	p.sport = uint16(l4[0])<<8 | uint16(l4[1])
	p.dport = uint16(l4[2])<<8 | uint16(l4[3])
	return p, true
}
