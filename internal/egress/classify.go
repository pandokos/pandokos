package egress

import "net/netip"

// AddrClass is the class of a network address.
type AddrClass int

// The classes of an address.
const (
	AddrPublic    AddrClass = iota // reachable by every endpoint
	AddrPrivate                    // reachable by private endpoints only
	AddrForbidden                  // never dialled
)

func prefixes(list ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(list))
	for i, s := range list {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

// The tables of sprint 3.1, section 5.5, built once.
var (
	forbiddenPrefixes = prefixes(
		"0.0.0.0/8", "::/128",
		"224.0.0.0/4", "ff00::/8",
		"240.0.0.0/4",
		"169.254.169.254/32", "169.254.170.2/32", "100.100.100.200/32", "fd00:ec2::254/128",
	)
	privatePrefixes = prefixes(
		"127.0.0.0/8", "::1/128",
		"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
		"100.64.0.0/10",
		"169.254.0.0/16", "fe80::/10",
		"fc00::/7", "fec0::/10",
		"192.0.0.0/24", "198.18.0.0/15",
		"192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24", "2001:db8::/32",
		"2001::/32", "64:ff9b:1::/48",
	)
	nat64Prefix = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour   = netip.MustParsePrefix("2002::/16")
)

func inAny(list []netip.Prefix, a netip.Addr) bool {
	for _, p := range list {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Classify returns the class of a. The table is that of sprint 3.1, section 5.5. An IPv4-mapped
// address is classified as the IPv4 address it holds, the zone is ignored, NAT64 and 6to4
// addresses take the class of the IPv4 address they embed, and an invalid address is forbidden.
func Classify(a netip.Addr) AddrClass {
	if !a.IsValid() {
		return AddrForbidden
	}
	a = a.Unmap().WithZone("")
	if inAny(forbiddenPrefixes, a) {
		return AddrForbidden
	}
	if a.Is6() {
		b := a.As16()
		switch {
		case nat64Prefix.Contains(a):
			return Classify(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
		case sixToFour.Contains(a):
			return Classify(netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}))
		}
	}
	if inAny(privatePrefixes, a) {
		return AddrPrivate
	}
	return AddrPublic
}
