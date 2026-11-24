package egress

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sync"
	"sync/atomic"
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

	blockedCount [6]atomic.Int64 // by reason, in the order of BlockReasons

	rootCAs  *x509.CertPool
	connect  time.Duration
	mu       sync.Mutex
	keyTrans []*http.Transport // the transports handed out by KeyService
}

// blocked counts a refusal and returns it as an error.
func (g *Guard) blocked(reason string, a netip.Addr) error {
	for i, r := range BlockReasons {
		if r == reason {
			g.blockedCount[i].Add(1)
			break
		}
	}
	return &BlockedError{Reason: reason, Addr: a}
}

// Stats is a snapshot of the counters of a Guard.
type Stats struct {
	Blocked map[string]int64 // refusals by reason, keyed by BlockReasons; every reason is present
}

// Stats returns a copy of the counters.
func (g *Guard) Stats() Stats {
	m := make(map[string]int64, len(BlockReasons))
	for i, r := range BlockReasons {
		m[r] = g.blockedCount[i].Load()
	}
	return Stats{Blocked: m}
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
	g := &Guard{classify: classify, rootCAs: o.RootCAs, connect: o.ConnectTimeout}

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
			return g.blocked(BlockNotTCP, netip.Addr{})
		}
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return g.blocked(BlockUnparsable, netip.Addr{})
		}
		return g.check(ap.Addr(), max)
	}
}

// check refuses a when its class is above max.
func (g *Guard) check(a netip.Addr, max AddrClass) error {
	a = a.Unmap().WithZone("")
	switch c := g.classify(a); {
	case c == AddrForbidden:
		return g.blocked(BlockForbiddenAddress, a)
	case c > max:
		return g.blocked(BlockPrivateAddress, a)
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

// KeyService returns a transport for the key service of the vault: its own connection pool,
// the private class, no proxy, no redirect, TLS 1.2 or later. roots, when not nil, are the only
// authorities trusted; nil means the roots of the guard.
func (g *Guard) KeyService(roots *x509.CertPool) http.RoundTripper {
	if roots == nil {
		roots = g.rootCAs
	}
	t := g.transport(Options{ConnectTimeout: g.connect, RootCAs: roots}, g.privateControl, false)
	t.Proxy = nil
	t.MaxIdleConns = 32
	t.MaxIdleConnsPerHost = 32
	g.mu.Lock()
	g.keyTrans = append(g.keyTrans, t)
	g.mu.Unlock()
	return &guardedTransport{g: g, tr: t, private: true}
}

// CloseIdleConnections closes the idle connections of both transports.
func (g *Guard) CloseIdleConnections() {
	g.public.CloseIdleConnections()
	g.private.CloseIdleConnections()
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, t := range g.keyTrans {
		t.CloseIdleConnections()
	}
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
		return t.g.blocked(BlockScheme, netip.Addr{})
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && t.private:
	default:
		return t.g.blocked(BlockScheme, netip.Addr{})
	}
	if u.User != nil {
		return t.g.blocked(BlockUserInfo, netip.Addr{})
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

// The values of BlockedError.Reason: the closed list behind the pandokos.egress.reason attribute
// of the metrics. No other value is used.
const (
	BlockNotTCP           = "not_tcp"
	BlockUnparsable       = "unparsable"
	BlockForbiddenAddress = "forbidden_address"
	BlockPrivateAddress   = "private_address"
	BlockScheme           = "scheme"
	BlockUserInfo         = "user_info"
)

// BlockReasons lists every reason a connection or URL is refused, in a fixed order.
var BlockReasons = []string{BlockNotTCP, BlockUnparsable, BlockForbiddenAddress, BlockPrivateAddress, BlockScheme, BlockUserInfo}

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
