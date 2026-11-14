package egress

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"
)

// Options configures a Guard.
type Options struct {
	ConnectTimeout time.Duration  // required, > 0
	RootCAs        *x509.CertPool // nil: the system roots
	Proxy          *url.URL       // nil: direct; used for public endpoints only
}

// Guard holds the two transports: one for public endpoints, one for private endpoints. The two
// never share a connection.
type Guard struct {
	classify func(netip.Addr) AddrClass

	publicControl  func(network, address string, c syscall.RawConn) error
	privateControl func(network, address string, c syscall.RawConn) error

	public  *http.Transport
	private *http.Transport

	publicRT  http.RoundTripper
	privateRT http.RoundTripper
}

// New returns a Guard.
func New(o Options) (*Guard, error) { return newGuard(o, Classify) }

func newGuard(o Options, classify func(netip.Addr) AddrClass) (*Guard, error) {
	if o.ConnectTimeout <= 0 {
		return nil, errors.New("egress: the connect timeout must be positive")
	}
	if o.Proxy != nil {
		if (o.Proxy.Scheme != "http" && o.Proxy.Scheme != "https") || o.Proxy.Host == "" {
			return nil, errors.New("egress: the proxy must be an http or https URL with a host")
		}
	}
	g := &Guard{classify: classify}

	// Without a proxy the public dialer reaches public addresses only. With one, the dialled
	// address is the proxy's, which is the operator's and often internal.
	publicMax := AddrPublic
	if o.Proxy != nil {
		publicMax = AddrPrivate
	}
	g.publicControl = g.control(publicMax)
	g.privateControl = g.control(AddrPrivate)

	g.public = g.transport(o, g.publicControl, true)
	g.private = g.transport(o, g.privateControl, false)
	g.publicRT = &guardedTransport{g: g, tr: g.public, private: false}
	g.privateRT = &guardedTransport{g: g, tr: g.private, private: true}
	return g, nil
}

func (g *Guard) transport(o Options, control func(string, string, syscall.RawConn) error, public bool) *http.Transport {
	d := &net.Dialer{Timeout: o.ConnectTimeout, KeepAlive: 30 * time.Second, Control: control}
	t := &http.Transport{
		DialContext:            d.DialContext,
		TLSHandshakeTimeout:    o.ConnectTimeout,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: o.RootCAs},
		ForceAttemptHTTP2:      true,
		MaxIdleConns:           256,
		MaxIdleConnsPerHost:    256,
		IdleConnTimeout:        90 * time.Second,
		ExpectContinueTimeout:  time.Second,
		DisableCompression:     true,
		MaxResponseHeaderBytes: 262144,
	}
	if public && o.Proxy != nil {
		t.Proxy = http.ProxyURL(o.Proxy)
	}
	return t
}

// control returns the control hook of a dialer that accepts addresses up to the class max.
// Forbidden addresses are always refused.
func (g *Guard) control(max AddrClass) func(string, string, syscall.RawConn) error {
	return func(network, address string, _ syscall.RawConn) error {
		if network != "tcp4" && network != "tcp6" {
			return &BlockedError{Reason: "not_tcp"}
		}
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return &BlockedError{Reason: "unparsable"}
		}
		return g.check(ap.Addr(), max)
	}
}

// check refuses a when its class is above max.
func (g *Guard) check(a netip.Addr, max AddrClass) error {
	a = a.Unmap().WithZone("")
	switch c := g.classify(a); {
	case c == AddrForbidden:
		return &BlockedError{Reason: "forbidden_address", Addr: a}
	case c > max:
		return &BlockedError{Reason: "private_address", Addr: a}
	}
	return nil
}

// Transport returns the round tripper for endpoints of one class; both live as long as the Guard.
func (g *Guard) Transport(private bool) http.RoundTripper {
	if private {
		return g.privateRT
	}
	return g.publicRT
}

// CloseIdleConnections closes the idle connections of both transports.
func (g *Guard) CloseIdleConnections() {
	g.public.CloseIdleConnections()
	g.private.CloseIdleConnections()
}

// guardedTransport runs the pre-flight checks before handing the request to the transport.
type guardedTransport struct {
	g       *Guard
	tr      *http.Transport
	private bool
}

// RoundTrip refuses, before any connection attempt, a scheme the class does not allow, user
// information in the URL, and an address literal the class may not reach.
func (t *guardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.preflight(req); err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	return t.tr.RoundTrip(req)
}

func (t *guardedTransport) preflight(req *http.Request) error {
	u := req.URL
	if u == nil {
		return &BlockedError{Reason: "scheme"}
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && t.private:
	default:
		return &BlockedError{Reason: "scheme"}
	}
	if u.User != nil {
		return &BlockedError{Reason: "user_info"}
	}
	if a, err := netip.ParseAddr(u.Hostname()); err == nil {
		max := AddrPublic
		if t.private {
			max = AddrPrivate
		}
		return t.g.check(a, max)
	}
	return nil
}

// BlockedError is a request the guard refused. Error() gives the reason and never the address.
type BlockedError struct {
	Reason string     // scheme, user_info, private_address, forbidden_address, not_tcp, unparsable
	Addr   netip.Addr // zero when the refusal came before an address was known
}

// Error returns a constant text built from the reason; it never holds an address.
func (e *BlockedError) Error() string { return "egress: blocked: " + e.Reason }

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
