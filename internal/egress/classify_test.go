package egress_test

import (
	"net/netip"
	"testing"

	"github.com/pandokos/pandokos/internal/egress"
)

func className(c egress.AddrClass) string {
	switch c {
	case egress.AddrPublic:
		return "public"
	case egress.AddrPrivate:
		return "private"
	case egress.AddrForbidden:
		return "forbidden"
	}
	return "unknown"
}

// lastOf returns the last address of a prefix.
func lastOf(p netip.Prefix) netip.Addr {
	a := p.Masked().Addr()
	b := a.AsSlice()
	for i := p.Bits(); i < len(b)*8; i++ {
		b[i/8] |= 1 << (7 - i%8)
	}
	out, _ := netip.AddrFromSlice(b)
	return out
}

// TestClassify is T-413: every range of sprint 3.1, section 5.5, at its first and last address
// and one inside; the neighbours of the ranges; mapped, embedded and zoned forms.
func TestClassify(t *testing.T) {
	type rng struct {
		prefix string
		inside string // an address inside that is not a more specific entry of the table
		want   egress.AddrClass
	}
	ranges := []rng{
		// forbidden
		{"0.0.0.0/8", "0.1.2.3", egress.AddrForbidden},
		{"::/128", "::", egress.AddrForbidden},
		{"224.0.0.0/4", "230.1.2.3", egress.AddrForbidden},
		{"ff00::/8", "ff02::1", egress.AddrForbidden},
		{"240.0.0.0/4", "250.1.2.3", egress.AddrForbidden},
		{"169.254.169.254/32", "169.254.169.254", egress.AddrForbidden},
		{"169.254.170.2/32", "169.254.170.2", egress.AddrForbidden},
		{"100.100.100.200/32", "100.100.100.200", egress.AddrForbidden},
		{"fd00:ec2::254/128", "fd00:ec2::254", egress.AddrForbidden},
		// private
		{"127.0.0.0/8", "127.1.2.3", egress.AddrPrivate},
		{"::1/128", "::1", egress.AddrPrivate},
		{"10.0.0.0/8", "10.20.30.40", egress.AddrPrivate},
		{"172.16.0.0/12", "172.20.1.1", egress.AddrPrivate},
		{"192.168.0.0/16", "192.168.1.1", egress.AddrPrivate},
		{"100.64.0.0/10", "100.64.0.1", egress.AddrPrivate},
		{"169.254.0.0/16", "169.254.1.1", egress.AddrPrivate},
		{"fe80::/10", "fe80::1234", egress.AddrPrivate},
		{"fc00::/7", "fd12:3456:789a::1", egress.AddrPrivate},
		{"fec0::/10", "fec0::1", egress.AddrPrivate},
		{"192.0.0.0/24", "192.0.0.8", egress.AddrPrivate},
		{"198.18.0.0/15", "198.19.1.1", egress.AddrPrivate},
		{"192.0.2.0/24", "192.0.2.77", egress.AddrPrivate},
		{"198.51.100.0/24", "198.51.100.77", egress.AddrPrivate},
		{"203.0.113.0/24", "203.0.113.77", egress.AddrPrivate},
		{"2001:db8::/32", "2001:db8:1::1", egress.AddrPrivate},
		{"2001::/32", "2001:0:4136:e378:8000:63bf:3fff:fdd2", egress.AddrPrivate},
		{"64:ff9b:1::/48", "64:ff9b:1::1234", egress.AddrPrivate},
	}
	// Prefixes that contain a forbidden entry have first and last addresses that are not that entry.
	for _, r := range ranges {
		p := netip.MustParsePrefix(r.prefix)
		for name, a := range map[string]netip.Addr{"first": p.Masked().Addr(), "last": lastOf(p), "inside": netip.MustParseAddr(r.inside)} {
			if got := egress.Classify(a); got != r.want {
				t.Errorf("%s of %s (%s): %s, want %s", name, r.prefix, a, className(got), className(r.want))
			}
		}
	}

	neighbours := []string{
		"126.255.255.255", "128.0.0.1", // around 127/8
		"9.255.255.255", "11.0.0.0", // 10/8
		"172.15.255.255", "172.32.0.0", // 172.16/12
		"192.167.255.255", "192.169.0.0", // 192.168/16
		"100.63.255.255", "100.128.0.0", // 100.64/10
		"169.253.255.255", "169.255.0.0", // 169.254/16
		"192.0.1.0", "192.0.3.0", // 192.0.0/24 and 192.0.2/24
		"198.17.255.255", "198.20.0.0", // 198.18/15
		"198.51.99.255", "198.51.101.0", // 198.51.100/24
		"203.0.112.255", "203.0.114.0", // 203.0.113/24
		"223.255.255.255", // below multicast
		"1.0.0.0", "8.8.8.8", "1.1.1.1", "93.184.216.34", "104.18.2.2", "172.67.1.1", "198.41.0.4",
		"2001:4860:4860::8888", "2606:4700:4700::1111", "2a00:1450:4001:81b::200e",
		"2001:1::1", "2001:db7:ffff::1", "2001:db9::1", "64:ff9b:2::1", "2000::1", "2400:cb00::1",
	}
	for _, s := range neighbours {
		if got := egress.Classify(netip.MustParseAddr(s)); got != egress.AddrPublic {
			t.Errorf("%s: %s, want public", s, className(got))
		}
	}

	forms := []struct {
		addr string
		want egress.AddrClass
		why  string
	}{
		{"::ffff:127.0.0.1", egress.AddrPrivate, "mapped loopback"},
		{"::ffff:10.1.2.3", egress.AddrPrivate, "mapped RFC 1918"},
		{"::ffff:8.8.8.8", egress.AddrPublic, "mapped public"},
		{"::ffff:169.254.169.254", egress.AddrForbidden, "mapped metadata"},
		{"::ffff:0.0.0.0", egress.AddrForbidden, "mapped unspecified"},
		{"::ffff:224.0.0.1", egress.AddrForbidden, "mapped multicast"},
		{"64:ff9b::808:808", egress.AddrPublic, "NAT64 of a public address"},
		{"64:ff9b::7f00:1", egress.AddrPrivate, "NAT64 of loopback"},
		{"64:ff9b::a00:1", egress.AddrPrivate, "NAT64 of RFC 1918"},
		{"64:ff9b::a9fe:a9fe", egress.AddrForbidden, "NAT64 of the metadata address"},
		{"64:ff9b::", egress.AddrForbidden, "NAT64 of 0.0.0.0"},
		{"2002:808:808::1", egress.AddrPublic, "6to4 of a public address"},
		{"2002:7f00:1::1", egress.AddrPrivate, "6to4 of loopback"},
		{"2002:c0a8:101::1", egress.AddrPrivate, "6to4 of 192.168.1.1"},
		{"2002:a9fe:a9fe::1", egress.AddrForbidden, "6to4 of the metadata address"},
		{"fe80::1%eth0", egress.AddrPrivate, "link-local with a zone"},
		{"::1%lo", egress.AddrPrivate, "loopback with a zone"},
		{"2001:4860:4860::8888%eth0", egress.AddrPublic, "public with a zone"},
		{"fd00:ec2::254%eth0", egress.AddrForbidden, "metadata with a zone"},
	}
	for _, f := range forms {
		if got := egress.Classify(netip.MustParseAddr(f.addr)); got != f.want {
			t.Errorf("%s (%s): %s, want %s", f.addr, f.why, className(got), className(f.want))
		}
	}

	if got := egress.Classify(netip.Addr{}); got != egress.AddrForbidden {
		t.Errorf("the zero Addr: %s, want forbidden", className(got))
	}
}
