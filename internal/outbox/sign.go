package outbox

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/secret"
)

// keyPrefix and keyLen are the text form of a key (Standard Webhooks): "whsec_" and the standard,
// padded base64 of 32 bytes.
const (
	keyPrefix = "whsec_" //nolint:gosec // a format label, not a credential
	keyLen    = 32
)

// Key is a signing key of a destination: 32 bytes. It prints nothing but the redaction marker on
// every formatting path.
type Key struct{ b [keyLen]byte }

var errKey = errors.New("outbox: not a signing secret of the form the Standard Webhooks specification gives")

// NewKey returns a key of 32 random bytes.
func NewKey() (Key, error) {
	var k Key
	if _, err := io.ReadFull(rand.Reader, k.b[:]); err != nil {
		return Key{}, fmt.Errorf("outbox: no random bytes: %w", err)
	}
	return k, nil
}

// ParseKey reads the text form of a key: "whsec_" and the standard, padded base64 of 32 bytes. The
// error quotes nothing of the text.
func ParseKey(text secret.Secret) (Key, error) {
	s := text.Reveal()
	rest, ok := strings.CutPrefix(s, keyPrefix)
	if !ok {
		return Key{}, errKey
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(rest)
	defer clear(raw)
	// The decoder skips line breaks: only the canonical text of the 32 bytes is a key.
	if err != nil || len(raw) != keyLen || base64.StdEncoding.EncodeToString(raw) != rest {
		return Key{}, errKey
	}
	var k Key
	copy(k.b[:], raw)
	return k, nil
}

// Clear zeroes the 32 bytes of the key. The text form it was read from is a Go string, which cannot
// be zeroed: only these bytes, and the buffers the vault and ParseKey clear themselves, are.
func (k *Key) Clear() { clear(k.b[:]) }

// Text returns the text form of the key.
func (k Key) Text() secret.Secret {
	return secret.New(keyPrefix + base64.StdEncoding.EncodeToString(k.b[:]))
}

// String returns the redaction marker.
func (Key) String() string { return secret.Redacted }

// GoString returns the redaction marker.
func (Key) GoString() string { return secret.Redacted }

// Format writes the redaction marker for every verb.
func (Key) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, secret.Redacted) }

// LogValue returns the redaction marker.
func (Key) LogValue() slog.Value { return slog.StringValue(secret.Redacted) }

// MarshalText returns the redaction marker.
func (Key) MarshalText() ([]byte, error) { return []byte(secret.Redacted), nil }

// MarshalJSON returns the redaction marker.
func (Key) MarshalJSON() ([]byte, error) { return []byte(`"` + secret.Redacted + `"`), nil }

// Sign returns the timestamp header and the signature header of a request that carries the event
// id at the time at with the given body, signed with every key (sprint 6.4, section 5.9): the
// timestamp is whole seconds since the epoch, and each signature is "v1," and the base64 of the
// HMAC-SHA256, under the key, of "<id>.<timestamp>.<body>"; several are separated by a space.
func Sign(id string, at time.Time, body []byte, keys ...Key) (timestamp, signature string) {
	timestamp = strconv.FormatInt(at.Unix(), 10)
	sigs := make([]string, 0, len(keys))
	for _, k := range keys {
		m := hmac.New(sha256.New, k.b[:])
		m.Write([]byte(id + "." + timestamp + "."))
		m.Write(body)
		sigs = append(sigs, "v1,"+base64.StdEncoding.EncodeToString(m.Sum(nil)))
	}
	return timestamp, strings.Join(sigs, " ")
}
