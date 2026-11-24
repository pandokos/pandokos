package vault

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/pandokos/pandokos/internal/secret"
)

// maxUnwrapEntries is the most unwrapped data keys the cache holds.
const maxUnwrapEntries = 10000

// Bounds of the lifetime of an entry.
const (
	minUnwrapTTL = time.Second
	maxUnwrapTTL = 15 * time.Minute
)

type unwrapEntry struct {
	key     []byte
	expires time.Time
}

// UnwrapCache wraps a KeyProvider and keeps what Unwrap returned for a short time, so the key
// service is not asked again for a data key in use. It is a KeyProvider itself. An entry is
// addressed by the digest of the version, the wrapped key and the associated data together, so a
// key is only served to a caller that presents the very same inputs; it is never used after its
// lifetime, and its bytes are zeroed when it expires or is dropped.
type UnwrapCache struct {
	p   KeyProvider
	ttl time.Duration
	now func() time.Time

	mu      sync.Mutex
	entries map[[sha256.Size]byte]*unwrapEntry
}

// NewUnwrapCache refuses a nil provider and a lifetime outside 1 s to 15 m.
func NewUnwrapCache(p KeyProvider, ttl time.Duration, now func() time.Time) (*UnwrapCache, error) {
	if p == nil {
		return nil, errors.New("vault: the unwrap cache needs a key provider")
	}
	if ttl < minUnwrapTTL || ttl > maxUnwrapTTL {
		return nil, errors.New("vault: the lifetime of the unwrap cache must be between 1 s and 15 m")
	}
	if now == nil {
		now = time.Now
	}
	return &UnwrapCache{p: p, ttl: ttl, now: now, entries: map[[sha256.Size]byte]*unwrapEntry{}}, nil
}

// Name returns the name of the provider behind the cache.
func (c *UnwrapCache) Name() string { return c.p.Name() }

// Wrap passes the call to the provider.
func (c *UnwrapCache) Wrap(ctx context.Context, plaintext, aad []byte) (string, []byte, error) {
	return c.p.Wrap(ctx, plaintext, aad)
}

// unwrapCacheKey is SHA-256 of the version, the wrapped key and the associated data, each with
// its length in front so no two different triples give one input.
func unwrapCacheKey(version string, wrapped, aad []byte) [sha256.Size]byte {
	h := sha256.New()
	var n [8]byte
	for _, part := range [][]byte{[]byte(version), wrapped, aad} {
		binary.BigEndian.PutUint64(n[:], uint64(len(part)))
		h.Write(n[:])
		h.Write(part)
	}
	var k [sha256.Size]byte
	h.Sum(k[:0])
	return k
}

// Unwrap serves the key from the cache or from the provider.
func (c *UnwrapCache) Unwrap(ctx context.Context, version string, wrapped, aad []byte) ([]byte, error) {
	k := unwrapCacheKey(version, wrapped, aad)
	c.mu.Lock()
	if e, ok := c.entries[k]; ok {
		if c.now().Before(e.expires) {
			out := bytes.Clone(e.key)
			c.mu.Unlock()
			return out, nil
		}
		clear(e.key)
		delete(c.entries, k)
	}
	c.mu.Unlock()

	key, err := c.p.Unwrap(ctx, version, wrapped, aad)
	if err != nil {
		return nil, err
	}
	out := bytes.Clone(key)
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.entries[k]; ok { // a concurrent miss stored it first
		clear(old.key)
	} else if len(c.entries) >= maxUnwrapEntries {
		for _, e := range c.entries {
			clear(e.key)
		}
		c.entries = map[[sha256.Size]byte]*unwrapEntry{}
	}
	c.entries[k] = &unwrapEntry{key: key, expires: c.now().Add(c.ttl)}
	return out, nil
}

// Available passes the call to the provider.
func (c *UnwrapCache) Available(ctx context.Context, version string) error {
	return c.p.Available(ctx, version)
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
