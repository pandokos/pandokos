package egress

import (
	"net/netip"
	"testing"
)

// addrOf makes an address out of the bytes the fuzzer gives: 16 bytes are an IPv6 address (the
// last 16 of a longer input, the first bytes zero-filled for a shorter one), and with v4 the first
// four are an IPv4 address.
func addrOf(b []byte, v4 bool) netip.Addr {
	var a16 [16]byte
	if len(b) >= 16 {
		copy(a16[:], b[len(b)-16:])
	} else {
		copy(a16[16-len(b):], b)
	}
	if v4 {
		var a4 [4]byte
		copy(a4[:], b)
		return netip.AddrFrom4(a4)
	}
	return netip.AddrFrom16(a16)
}

// FuzzClassify is T-1115: Classify is total over 16 bytes and a flag for IPv4. The class of an
// IPv4-mapped address is the class of the IPv4 address; a NAT64 or 6to4 address of a forbidden or
// private IPv4 address is not public; every address inside a forbidden prefix is forbidden; a zone
// changes nothing; the invalid address is forbidden.
func FuzzClassify(f *testing.F) {
	for _, s := range []string{
		"169.254.169.254", "169.254.170.2", "100.100.100.200", "127.0.0.1", "10.1.2.3", "8.8.8.8", "0.0.0.0", "224.0.0.1", "255.255.255.255",
		"fd00:ec2::254", "::1", "::", "fe80::1", "2001:4860:4860::8888", "64:ff9b::a9fe:a9fe", "64:ff9b::808:808", "2002:a9fe:a9fe::1",
		"2002:808:808::1", "::ffff:169.254.169.254", "::ffff:8.8.8.8", "::7f00:1", "::ffff:0:a00:1", "64:ff9b:1::1", "fec0::1",
	} {
		a := netip.MustParseAddr(s)
		b := a.As16()
		f.Add(b[:], a.Is4())
		f.Add(b[:], false)
		if a.Is4() {
			f.Add(a.AsSlice(), true)
		}
	}
	f.Add([]byte{}, false)
	f.Add([]byte{1}, true)
	f.Fuzz(func(t *testing.T, raw []byte, v4 bool) {
		if len(raw) > 1<<10 {
			t.Skip()
		}
		a := addrOf(raw, v4)
		c := Classify(a)
		if c != AddrPublic && c != AddrPrivate && c != AddrForbidden {
			t.Fatalf("%v: the class %v is not one of the three", a, c)
		}
		if Classify(a.WithZone("eth0")) != c || Classify(a) != c {
			t.Fatalf("%v: the class depends on a zone or changes between calls", a)
		}
		if Classify(netip.Addr{}) != AddrForbidden {
			t.Fatal("the invalid address is not forbidden")
		}
		for _, p := range forbiddenPrefixes {
			if p.Contains(a.Unmap()) && c != AddrForbidden {
				t.Fatalf("%v is inside the forbidden prefix %v and its class is %v", a, p, c)
			}
		}
		if a.Is4() {
			// The IPv4-mapped form has the class of the IPv4 address.
			b := a.As4()
			mapped := netip.AddrFrom16([16]byte{10: 0xff, 11: 0xff, 12: b[0], 13: b[1], 14: b[2], 15: b[3]})
			if got := Classify(mapped); got != c {
				t.Fatalf("%v is %v and its mapped form %v is %v", a, c, mapped, got)
			}
			// NAT64 and 6to4 forms of a forbidden or private address are not public.
			nat64 := netip.AddrFrom16([16]byte{0x00, 0x64, 0xff, 0x9b, 12: b[0], 13: b[1], 14: b[2], 15: b[3]})
			sixToFour := netip.AddrFrom16([16]byte{0x20, 0x02, b[0], b[1], b[2], b[3], 15: 1})
			if c != AddrPublic {
				for name, e := range map[string]netip.Addr{"NAT64": nat64, "6to4": sixToFour} {
					if got := Classify(e); got == AddrPublic {
						t.Fatalf("%v is %v and its %s form %v is public", a, c, name, e)
					}
				}
			}
			if c == AddrPublic {
				if got := Classify(nat64); got != AddrPublic {
					t.Fatalf("a public %v has a NAT64 form %v of class %v", a, nat64, got)
				}
			}
		}
	})
}
