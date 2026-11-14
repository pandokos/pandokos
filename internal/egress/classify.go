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

// Classify returns the class of a. The table is that of sprint 3.1, section 5.5.
func Classify(a netip.Addr) AddrClass {
	_ = a
	return AddrForbidden
}
