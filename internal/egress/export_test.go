package egress

import (
	"net/http"
	"net/netip"
	"syscall"
)

// ControlFunc is the type of the control hook of a dialer.
type ControlFunc = func(network, address string, c syscall.RawConn) error

// NewGuardWithClassifier builds a guard whose address classification is replaced by classify.
// The unexported field exists for tests only: nothing outside this package can set it, so no
// configuration of the product can relax the guard (sprint 3.1, plan part B3). Part C1
// implements newGuard(o Options, classify func(netip.Addr) AddrClass) (*Guard, error); New is
// newGuard with Classify.
func NewGuardWithClassifier(o Options, classify func(netip.Addr) AddrClass) (*Guard, error) {
	return newGuard(o, classify)
}

// PublicControl returns the control hook of the dialer of the public transport.
func (g *Guard) PublicControl() ControlFunc { return g.publicControl }

// PrivateControl returns the control hook of the dialer of the private transport.
func (g *Guard) PrivateControl() ControlFunc { return g.privateControl }

// PublicTransport returns the *http.Transport behind Transport(false).
func (g *Guard) PublicTransport() *http.Transport { return g.public }

// PrivateTransport returns the *http.Transport behind Transport(true).
func (g *Guard) PrivateTransport() *http.Transport { return g.private }
