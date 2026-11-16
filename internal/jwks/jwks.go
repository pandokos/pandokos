// Package jwks retrieves and caches the verification keys of the token issuers a gateway trusts
// (sprint 4.1, section 5.5). It is the only package that fetches an issuer's documents, and it does
// so only through the transport function it is given. In this scaffolding the cache holds no keys:
// Key always answers that the keys cannot be obtained now; the retrieval arrives with its tests.
package jwks

import (
	"context"
	"crypto"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	// The JOSE library of the sprint: it is imported here so that go mod tidy keeps it until the
	// retrieval of keys uses it.
	_ "github.com/go-jose/go-jose/v4"

	"github.com/pandokos/pandokos/internal/authz"
)

// ErrKeyNotFound is returned by Key when the issuer's key set has no usable key for the token. It
// is a refusal of the token, not an outage.
var ErrKeyNotFound = errors.New("jwks: no usable key for this token")

// Source says where the keys of one issuer are: its URL, and the address of its key set.
type Source struct {
	Issuer  string // the issuer URL of the registration
	JWKSURI string // the address of the key set; empty: OpenID Connect discovery under the issuer
}

// Options configure a Cache.
type Options struct {
	// Transport returns the round tripper that fetches the issuer's documents; private says that
	// the host is one of tokens.private_hosts. Required.
	Transport func(private bool) http.RoundTripper
	// PrivateHosts are the lower-case host names that may resolve to private addresses.
	PrivateHosts []string
	// FetchTimeout bounds one fetch; RefreshInterval is the period of the background refresh;
	// MinRefetchInterval spaces the refetches caused by an unknown key ID, per source; MaxStale is
	// how long keys are used after the last successful fetch. All must be positive, and MaxStale at
	// least twice RefreshInterval.
	FetchTimeout, RefreshInterval, MinRefetchInterval, MaxStale time.Duration
	Logger                                                      *slog.Logger // required
	Now                                                         func() time.Time
}

// Cache holds the keys of the issuers.
type Cache struct {
	o Options
}

// New validates o and returns a Cache.
func New(o Options) (*Cache, error) {
	switch {
	case o.Transport == nil:
		return nil, errors.New("jwks: Transport is required")
	case o.Logger == nil:
		return nil, errors.New("jwks: Logger is required")
	case o.FetchTimeout <= 0 || o.RefreshInterval <= 0 || o.MinRefetchInterval <= 0 || o.MaxStale <= 0:
		return nil, errors.New("jwks: every duration must be positive")
	case o.MaxStale < 2*o.RefreshInterval:
		return nil, fmt.Errorf("jwks: MaxStale must be at least twice RefreshInterval")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Cache{o: o}, nil
}

// Key returns the key that verifies a token signed with alg and the key ID kid. The error is
// ErrKeyNotFound for a refusal, and wraps authz.ErrUnavailable when the keys cannot be obtained
// now. The scaffolding has no keys and always answers the latter.
func (c *Cache) Key(_ context.Context, _ Source, _, _ string) (crypto.PublicKey, error) {
	return nil, fmt.Errorf("%w: key retrieval is not implemented", authz.ErrUnavailable)
}
