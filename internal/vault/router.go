package vault

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"

	"github.com/pandokos/pandokos/internal/secret"
)

// Router sends every call to the provider that owns the key version: the active provider wraps,
// every provider unwraps the versions it owns. The owner is named by the part of the version
// identifier before its first "-"; an identifier of no configured provider is unknown, and no
// call for it reaches any provider.
type Router struct {
	active KeyProvider
	byName map[string]KeyProvider
}

// NewRouter refuses a nil provider and two providers of one name.
func NewRouter(active KeyProvider, others ...KeyProvider) (*Router, error) {
	if active == nil {
		return nil, errors.New("vault: a router needs an active key provider")
	}
	r := &Router{active: active, byName: map[string]KeyProvider{}}
	for _, p := range append([]KeyProvider{active}, others...) {
		if p == nil {
			return nil, errors.New("vault: a router cannot hold a nil key provider")
		}
		name := p.Name()
		if name == "" || strings.Contains(name, "-") {
			return nil, errors.New("vault: a key provider needs a name without a dash")
		}
		if _, dup := r.byName[name]; dup {
			return nil, fmt.Errorf("vault: two key providers are named %s", name)
		}
		r.byName[name] = p
	}
	return r, nil
}

// Name returns the name of the active provider.
func (r *Router) Name() string { return r.active.Name() }

// Wrap wraps with the active provider and refuses a version that does not start with its name.
func (r *Router) Wrap(ctx context.Context, plaintext, aad []byte) (string, []byte, error) {
	version, wrapped, err := r.active.Wrap(ctx, plaintext, aad)
	if err != nil {
		return "", nil, err
	}
	if !strings.HasPrefix(version, r.active.Name()+"-") {
		clear(wrapped)
		return "", nil, errors.New("vault: the key provider returned a key version it does not own")
	}
	return version, wrapped, nil
}

// owner returns the provider named by the identifier, or nil.
func (r *Router) owner(version string) KeyProvider {
	i := strings.IndexByte(version, '-')
	if i <= 0 {
		return nil
	}
	return r.byName[version[:i]]
}

// Unwrap unwraps with the provider that owns version.
func (r *Router) Unwrap(ctx context.Context, version string, wrapped, aad []byte) ([]byte, error) {
	p := r.owner(version)
	if p == nil {
		return nil, ErrKeyVersionUnknown
	}
	return p.Unwrap(ctx, version, wrapped, aad)
}

// Available reports whether Unwrap can serve version now.
func (r *Router) Available(ctx context.Context, version string) error {
	p := r.owner(version)
	if p == nil {
		return ErrKeyVersionUnknown
	}
	return p.Available(ctx, version)
}

// Others returns the providers that are not the active one, in the order of their names.
func (r *Router) Others() []KeyProvider {
	names := make([]string, 0, len(r.byName))
	for n := range r.byName {
		if n != r.active.Name() {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	out := make([]KeyProvider, 0, len(names))
	for _, n := range names {
		out = append(out, r.byName[n])
	}
	return out
}

// ProviderOf returns the name of the provider that owns version, or "".
func (r *Router) ProviderOf(version string) string {
	if p := r.owner(version); p != nil {
		return p.Name()
	}
	return ""
}

// String returns the redaction marker.
func (*Router) String() string { return secret.Redacted }

// GoString returns the redaction marker.
func (*Router) GoString() string { return secret.Redacted }

// Format writes the redaction marker for every verb.
func (*Router) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, secret.Redacted) }

// LogValue returns the redaction marker as a log value.
func (*Router) LogValue() slog.Value { return slog.StringValue(secret.Redacted) }

// MarshalJSON returns the redaction marker as a JSON string.
func (*Router) MarshalJSON() ([]byte, error) { return []byte(`"` + secret.Redacted + `"`), nil }

// MarshalText returns the redaction marker.
func (*Router) MarshalText() ([]byte, error) { return []byte(secret.Redacted), nil }
