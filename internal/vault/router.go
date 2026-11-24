package vault

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/pandokos/pandokos/internal/secret"
)

// Router sends every call to the provider that owns the key version: the active provider wraps,
// every provider unwraps the versions it owns.
//
//nolint:unused // scaffolding: the fields are used from step C6
type Router struct {
	active   KeyProvider
	byName   map[string]KeyProvider
	versions map[string]string // key version identifier to provider name
}

// NewRouter refuses a nil provider and two providers of one name.
func NewRouter(_ KeyProvider, _ ...KeyProvider) (*Router, error) {
	return nil, errors.New("not implemented")
}

// Name returns the name of the active provider.
func (r *Router) Name() string { return "" }

// Wrap wraps with the active provider.
func (r *Router) Wrap(_ context.Context, _, _ []byte) (string, []byte, error) {
	return "", nil, errors.New("not implemented")
}

// Unwrap unwraps with the provider that owns version.
func (r *Router) Unwrap(_ context.Context, _ string, _, _ []byte) ([]byte, error) {
	return nil, errors.New("not implemented")
}

// Available reports whether Unwrap can serve version now.
func (r *Router) Available(_ context.Context, _ string) error {
	return errors.New("not implemented")
}

// ProviderOf returns the name of the provider that owns version, or "".
func (r *Router) ProviderOf(_ string) string { return "" }

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
