package admin

import (
	"net/http"
	"net/netip"
)

// expectedOrigin is the origin that a browser reaches the administration API at (section 5.7), as a
// function of the request and the options; ok is false when the listener cannot say (plain HTTP
// outside loopback without auth.public_origin). Part B gives it no behaviour; step C3 writes it.
func expectedOrigin(r *http.Request, o OriginOptions) (origin string, ok bool) {
	_, _ = r, o
	return "", false
}

// sourceAddr is the address that a sign-in is counted against (section 5.5): the peer's, or, when
// the peer is a trusted proxy, the right-most entry of X-Forwarded-For that is not one. Part B gives
// it no behaviour; step C3 writes it.
func sourceAddr(r *http.Request, o OriginOptions) netip.Addr {
	_, _ = r, o
	return netip.Addr{}
}
