package egress

import (
	"crypto/x509"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"time"
)

// Options configures a Guard.
type Options struct {
	ConnectTimeout time.Duration  // required, > 0
	RootCAs        *x509.CertPool // nil: the system roots
	Proxy          *url.URL       // nil: direct; used for public endpoints only
}

// Guard holds the two transports: one for public endpoints, one for private endpoints.
type Guard struct{}

// New returns a Guard.
func New(o Options) (*Guard, error) {
	_ = o
	return nil, errors.New("egress: not implemented")
}

// Transport returns the round tripper for endpoints of one class; both live as long as the Guard.
func (g *Guard) Transport(private bool) http.RoundTripper {
	_ = private
	return nil
}

// CloseIdleConnections closes the idle connections of both transports.
func (g *Guard) CloseIdleConnections() {}

// BlockedError is a request the guard refused. Error() gives the reason and never the address.
type BlockedError struct {
	Reason string     // scheme, user_info, private_address, forbidden_address, not_tcp, unparsable
	Addr   netip.Addr // zero when the refusal came before an address was known
}

// Error returns a constant text built from the reason; it never holds an address.
func (e *BlockedError) Error() string { return "egress: request blocked: " + e.Reason }

// BlockedReason returns the reason of the refusal.
func (e *BlockedError) BlockedReason() string { return e.Reason }

// BlockedAddr returns the refused address, or "" when Addr is the zero value. It lets a
// package that may not import egress recognize a refusal through an interface of its own.
func (e *BlockedError) BlockedAddr() string {
	if !e.Addr.IsValid() {
		return ""
	}
	return e.Addr.String()
}
