package vault

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/pandokos/pandokos/internal/secret"
)

// maxUnwrapEntries is the most unwrapped data keys the cache holds.
const maxUnwrapEntries = 10000 //nolint:unused // scaffolding: used from step C6

// UnwrapCache wraps a KeyProvider and keeps what Unwrap returned for a short time, so the key
// service is not asked again for a data key in use. It is a KeyProvider itself.
//
//nolint:unused // scaffolding: the fields are used from step C6
type UnwrapCache struct {
	p   KeyProvider
	ttl time.Duration
	now func() time.Time
}

// NewUnwrapCache refuses a nil provider and a lifetime outside 1 s to 15 m.
func NewUnwrapCache(_ KeyProvider, _ time.Duration, _ func() time.Time) (*UnwrapCache, error) {
	return nil, errors.New("not implemented")
}

// Name returns the name of the provider behind the cache.
func (c *UnwrapCache) Name() string { return "" }

// Wrap passes the call to the provider.
func (c *UnwrapCache) Wrap(_ context.Context, _, _ []byte) (string, []byte, error) {
	return "", nil, errors.New("not implemented")
}

// Unwrap serves the key from the cache or from the provider.
func (c *UnwrapCache) Unwrap(_ context.Context, _ string, _, _ []byte) ([]byte, error) {
	return nil, errors.New("not implemented")
}

// Available passes the call to the provider.
func (c *UnwrapCache) Available(_ context.Context, _ string) error {
	return errors.New("not implemented")
}

// String returns the redaction marker.
func (*UnwrapCache) String() string { return secret.Redacted }

// GoString returns the redaction marker.
func (*UnwrapCache) GoString() string { return secret.Redacted }

// Format writes the redaction marker for every verb.
func (*UnwrapCache) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, secret.Redacted) }

// LogValue returns the redaction marker as a log value.
func (*UnwrapCache) LogValue() slog.Value { return slog.StringValue(secret.Redacted) }

// MarshalJSON returns the redaction marker as a JSON string.
func (*UnwrapCache) MarshalJSON() ([]byte, error) { return []byte(`"` + secret.Redacted + `"`), nil }

// MarshalText returns the redaction marker.
func (*UnwrapCache) MarshalText() ([]byte, error) { return []byte(secret.Redacted), nil }
