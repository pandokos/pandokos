package jwks_test

import (
	"bytes"
	"context"
	"crypto"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/jwks"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/test/fakeidp"
	"github.com/pandokos/pandokos/test/testutil"
)

// clock is a clock the test moves.
type clock struct{ ns atomic.Int64 }

func newClock() *clock {
	c := &clock{}
	c.ns.Store(time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC).UnixNano())
	return c
}
func (c *clock) now() time.Time          { return time.Unix(0, c.ns.Load()).UTC() }
func (c *clock) advance(d time.Duration) { c.ns.Add(int64(d)) }

// The intervals of the tests: the defaults of the configuration.
const (
	refreshInterval = 15 * time.Minute
	minRefetch      = 10 * time.Second
	maxStale        = 6 * time.Hour
)

// rig is a cache over a transport, with a clock the test moves and the log it writes.
type rig struct {
	c    *jwks.Cache
	clk  *clock
	logs *testutil.LogBuffer
}

// newRig builds a cache; mutate may change the options before it is built.
func newRig(t testing.TB, transport func(private bool) http.RoundTripper, mutate ...func(*jwks.Options)) *rig {
	t.Helper()
	r := &rig{clk: newClock(), logs: &testutil.LogBuffer{}}
	o := jwks.Options{
		Transport:          transport,
		FetchTimeout:       time.Second,
		RefreshInterval:    refreshInterval,
		MinRefetchInterval: minRefetch,
		MaxStale:           maxStale,
		Logger:             telemetry.NewLogger(r.logs, "debug"),
		Now:                r.clk.now,
	}
	for _, m := range mutate {
		m(&o)
	}
	var err error
	if r.c, err = jwks.New(o); err != nil {
		t.Fatal(err)
	}
	return r
}

// overFake builds a cache whose transport trusts the fake issuer's authority, for both classes.
func overFake(t testing.TB, idp *fakeidp.Server, mutate ...func(*jwks.Options)) *rig {
	t.Helper()
	rt := idp.Transport()
	return newRig(t, func(bool) http.RoundTripper { return rt }, mutate...)
}

func (r *rig) key(_ *fakeidp.Server, src jwks.Source, kid, alg string) (crypto.PublicKey, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return r.c.Key(ctx, src, kid, alg)
}

// discover is the source of the fake issuer by discovery.
func discover(idp *fakeidp.Server) jwks.Source { return jwks.Source{Issuer: idp.Issuer()} }

// explicit is the source of the fake issuer with its key set address.
func explicit(idp *fakeidp.Server) jwks.Source {
	return jwks.Source{Issuer: idp.Issuer(), JWKSURI: idp.JWKSURI()}
}

func isUnavailable(err error) bool { return errors.Is(err, authz.ErrUnavailable) }

// failureClasses returns the "class" of every "issuer keys not fetched" line.
func (r *rig) failureClasses(t testing.TB) []string {
	t.Helper()
	var out []string
	for _, l := range r.logs.Find(t, "issuer keys not fetched") {
		c, _ := l["class"].(string)
		out = append(out, c)
	}
	return out
}

// docs is a RoundTripper that answers from memory and records what it was asked: for the tests
// that need a document the fake issuer cannot make, and no network.
type docs struct {
	mu        sync.Mutex
	discovery func(issuerHost string) []byte // nil: 404
	keys      []byte                         // the key set; nil: 404
	status    int                            // 0: 200
	seen      []seenRequest
	private   bool
}

type seenRequest struct {
	Method, Scheme, Host, Path, Accept string
	Header                             http.Header
	Private                            bool
}

func (d *docs) RoundTrip(req *http.Request) (*http.Response, error) {
	d.mu.Lock()
	d.seen = append(d.seen, seenRequest{req.Method, req.URL.Scheme, req.URL.Host, req.URL.Path, req.Header.Get("Accept"), req.Header.Clone(), d.private})
	status, discovery, keys := d.status, d.discovery, d.keys
	d.mu.Unlock()
	body := []byte(nil)
	code := http.StatusNotFound
	switch {
	case strings.HasSuffix(req.URL.Path, "/.well-known/openid-configuration") && discovery != nil:
		body, code = discovery(req.URL.Host), http.StatusOK
	case strings.HasSuffix(req.URL.Path, "jwks.json") && keys != nil:
		body, code = keys, http.StatusOK
	}
	if status != 0 {
		code = status
	}
	return &http.Response{
		StatusCode: code, Status: http.StatusText(code), Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)), Request: req,
	}, nil
}

func (d *docs) requests() []seenRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]seenRequest(nil), d.seen...)
}

// classRecorder returns a transport function that hands out two docs, one per class, so that a
// test sees which class served each request.
type classRecorder struct{ public, private *docs }

func newClassRecorder(discovery func(string) []byte, keys []byte) *classRecorder {
	return &classRecorder{
		public:  &docs{discovery: discovery, keys: keys},
		private: &docs{discovery: discovery, keys: keys, private: true},
	}
}

func (c *classRecorder) transport(private bool) http.RoundTripper {
	if private {
		return c.private
	}
	return c.public
}

func cancelAfter(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
